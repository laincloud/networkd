package main

import (
	"fmt"
	"github.com/Sirupsen/logrus"
	"github.com/projectcalico/libcalico-go/lib/api"
	"github.com/projectcalico/libcalico-go/lib/numorstring"
	"strconv"
)

func (s *Agent) AddCalicoRule(ruleType, profileName, action, protocol, port string) error {

	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		log.WithFields(logrus.Fields{
			"err": err,
		}).Error("port " + port + " is not valid")
		return err
	}

	if ruleType != "ingress" && ruleType != "egress" {
		log.Error("ruleType " + ruleType + " is not ingress or egress ")
		return fmt.Errorf("ruleType %s is not ingress or egress", ruleType)
	}

	profile, err := s.calico.Profiles().Get(api.ProfileMetadata{Name: profileName})
	if err != nil {
		log.WithFields(logrus.Fields{
			"err": err,
		}).Error("get profile " + profileName + " error")
		return err
	}
	log.WithField("profile", *profile).Info("calico profile dump after get")

	if ruleType == "ingress" {
		var rules []api.Rule
		ingressRules := profile.Spec.IngressRules
		for _, rule := range ingressRules {
			ingressRule := rule
			if len(ingressRule.Destination.Ports)  > 0 {
				for _, destPort := range ingressRule.Destination.Ports {
					for i := destPort.MinPort; i <= destPort.MaxPort; i++ {
						if uint64(i) == p {
							continue
						}
						tmpRule := ingressRule
						tmpRule.Destination.Ports = []numorstring.Port{numorstring.SinglePort(i)}
						rules = append(rules, tmpRule)
					}
				}
			} else {
				rules = append(rules, ingressRule)
			}
		}
		rule := api.Rule{}
		rule.Action = action
		proto := numorstring.ProtocolFromString(protocol)
		rule.Protocol = &proto
		rule.Destination.Ports = []numorstring.Port{numorstring.SinglePort(uint16(p))}
		rules = append(rules, rule)
		profile.Spec.IngressRules = rules
	}

	if ruleType == "egress" {
		var rules []api.Rule
		egressRules := profile.Spec.EgressRules
		for _, rule := range egressRules {
			egressRule := rule
			if len(egressRule.Destination.Ports) > 0 {
				for _, destPort := range egressRule.Destination.Ports {
					for i := destPort.MinPort; i <= destPort.MaxPort; i++ {
						if uint64(i) == p {
							continue
						}
						tmpRule := egressRule
						tmpRule.Destination.Ports = []numorstring.Port{numorstring.SinglePort(i)}
						rules = append(rules, tmpRule)
					}
				}
			} else {
				rules = append(rules, egressRule)
			}
		}
		rule := api.Rule{}
		rule.Action = action
		proto := numorstring.ProtocolFromString(protocol)
		rule.Protocol = &proto
		rule.Destination.Ports = []numorstring.Port{numorstring.SinglePort(uint16(p))}
		rules = append(rules, rule)
		profile.Spec.EgressRules = rules
	}

	log.WithField("profile", *profile).Info("calico profile dump before set")
	_, err = s.calico.Profiles().Apply(profile)
	if err != nil {
		log.WithFields(logrus.Fields{
			"err": err,
		}).Error("apply profile " + profileName + " error")
		return err
	}
	return nil
}

func (s *Agent) ApplyCalicoProfile(virtualIpItem *VirtualIpItem) {
	for _, v := range virtualIpItem.ports {
		s.AddCalicoRule("ingress", virtualIpItem.appName, "allow", v.proto, v.port)
	}
}
