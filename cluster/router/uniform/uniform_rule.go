/*
 * Licensed to the Apache Software Foundation (ASF) under one or more
 * contributor license agreements.  See the NOTICE file distributed with
 * this work for additional information regarding copyright ownership.
 * The ASF licenses this file to You under the Apache License, Version 2.0
 * (the "License"); you may not use this file except in compliance with
 * the License.  You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package uniform

import (
	"fmt"
)

import (
	perrors "github.com/pkg/errors"
)

import (
	"github.com/apache/dubbo-go/cluster/router/uniform/match_judger"
	"github.com/apache/dubbo-go/common"
	"github.com/apache/dubbo-go/common/logger"
	"github.com/apache/dubbo-go/config"
	"github.com/apache/dubbo-go/protocol"
)

// VirtualServiceRule is item of virtual service, it aims at judge if invocation context match it's condition, and
// if match, get result destination key, which should be defined in DestinationRule yaml file
type VirtualServiceRule struct {
	// routerItem store match router list and destination list of this router
	routerItem *config.DubboServiceRouterItem

	// uniformRule is the upper struct ptr
	uniformRule *UniformRule
}

// match read from vsr's Match config
// it judges if this invocation matches the router rule request defined in config one by one
func (vsr *VirtualServiceRule) match(url *common.URL, invocation protocol.Invocation) bool {
	fmt.Println(url, invocation)
	for _, v := range vsr.routerItem.Match {
		// method match judge
		if v.Method != nil {
			methodMatchJudger := match_judger.NewMethodMatchJudger(v.Method)
			if !methodMatchJudger.Judge(invocation) {
				return false
			}
		}

		// source label match judge
		// todo

		// atta match judge
		if v.Attachment != nil {
			attachmentMatchJudger := match_judger.NewAttachmentMatchJudger(v.Attachment)
			if attachmentMatchJudger.Judge(invocation) {
				return false
			}
		}
		// threshold match judge
		// todo

		// reserve match judge
		// todo
	}
	return true
}

// tryGetSubsetFromRouterOfOneDestination is a recursion function
// try from destination 's header to final fallback destination, when success, it return result destination, else return error
func (vsr *VirtualServiceRule) tryGetSubsetFromRouterOfOneDestination(desc *config.DubboDestination, invokers []protocol.Invoker) ([]protocol.Invoker, error) {
	subSet := desc.Destination.Subset
	labels, ok := vsr.uniformRule.DestinationLabelListMap[subSet]
	resultInvokers := make([]protocol.Invoker, 0)
	if ok {
		for _, v := range invokers {
			if match_judger.JudgeUrlLabel(v.GetUrl(), labels) {
				resultInvokers = append(resultInvokers, v)
			}
		}
		if len(resultInvokers) != 0 {
			return resultInvokers, nil
		}
	}

	if desc.Fallback != nil {
		return vsr.tryGetSubsetFromRouterOfOneDestination(desc.Fallback, invokers)
	}
	return nil, perrors.New("No invoker matches and no fallback destination to choose!")
}

func (vsr *VirtualServiceRule) getRuleTargetInvokers(invokers []protocol.Invoker) ([]protocol.Invoker, error) {
	// descResultList is the collection routerDesc of all destination fields,
	invokerList := make([]protocol.Invoker, 0)
	for _, v := range vsr.routerItem.Router {
		// v is one destination 's header e.g.
		/*
			   route:
				 - destination:      # v is here
					 host: demo
					 subset: v1
				   fallback:
					 destination:
					   host: demo
					   subset: v2
					 fallback:
					   destination:
						 host: demo
						 subset: v3
				 - destination:
					 host: demo
					 subset: v4
				   fallback:
					 destination:
					   host: demo
					   subset: v5
					 fallback:
					   destination:
						 host: demo
						 subset: v6
		*/
		invokerListOfOneDest, err := vsr.tryGetSubsetFromRouterOfOneDestination(v, invokers)
		if err != nil {
			return nil, err
		}
		// combination of all destination field e.g.
		/*
			 - destination:
			   host: demo
			   subset: na61
			- destination:
			   host: demo
			   subset: na610
		*/
		invokerList = append(invokerList, invokerListOfOneDest...)
	}
	// delete equal invoker
	resultInvokersMap := make(map[string]protocol.Invoker)
	for _, v := range invokerList {
		resultInvokersMap[v.GetUrl().Key()] = v
	}
	invokerList = make([]protocol.Invoker, 0)
	for _, v := range resultInvokersMap {
		invokerList = append(invokerList, v)
	}
	return invokerList, nil
}

// UniformRule
type UniformRule struct {
	services                []*config.StringMatch
	virtualServiceRules     []VirtualServiceRule
	DestinationLabelListMap map[string]map[string]string
}

// NewDefaultConnChecker constructs a new DefaultConnChecker based on the url
func newUniformRule(dubboRoute *config.DubboRoute, destinationMap map[string]map[string]string) (*UniformRule, error) {
	matchItems := dubboRoute.RouterDetail
	virtualServiceRules := make([]VirtualServiceRule, 0)
	newUniformRule := &UniformRule{
		DestinationLabelListMap: destinationMap,
		services:                dubboRoute.Services,
	}
	for _, v := range matchItems {
		virtualServiceRules = append(virtualServiceRules, VirtualServiceRule{
			routerItem:  v,
			uniformRule: newUniformRule,
		})
	}
	newUniformRule.virtualServiceRules = virtualServiceRules
	return newUniformRule, nil
}

func (u *UniformRule) route(invokers []protocol.Invoker, url *common.URL, invocation protocol.Invocation) []protocol.Invoker {
	// service rule + destination -> filter
	resultInvokers := make([]protocol.Invoker, 0)
	matchService := false
	for _, v := range u.services {
		// check if match service field
		if match_judger.NewStringMatchJudger(v).Judge(url.ServiceKey()) {
			matchService = true
			break
		}
	}
	if !matchService {
		// if not match, jump this rule
		return resultInvokers
	}
	// match service field, route Details level(service level) match
	// then, check all sub rule, if match, get destination rule target invokers, else do fail back logic
	for _, rule := range u.virtualServiceRules {
		if rule.match(url, invocation) {
			// match this rule, do get target logic
			resultInvokers, err := rule.getRuleTargetInvokers(invokers)
			if err != nil {
				logger.Error("getRuleTargetInvokers from rule err = ", err)
				return nil
			}
			return resultInvokers
		}
	}
	logger.Error("no match rule!")
	return resultInvokers
}
