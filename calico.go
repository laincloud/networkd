package main

import (
	"github.com/projectcalico/libcalico-go/lib/api"
	"github.com/projectcalico/libcalico-go/lib/numorstring"
	"strconv"
)

func (s *Server) AddCalicoRule(profileName string, action string, protocol string, port string) bool {
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		log.Fatal(err)
	}
	profile, err := s.calico.Profiles().Get(api.ProfileMetadata{Name: profileName})
	if err != nil {
		log.Fatal(err)
	}
	rule := api.Rule{}
	rule.Action = action
	proto := numorstring.ProtocolFromString(protocol)
	rule.Protocol = &proto
	rule.Destination.Ports = []numorstring.Port{numorstring.SinglePort(uint16(p))}
	if action == "allow" {
		rules := []api.Rule{rule}
		for _, ingressRule := range profile.Spec.IngressRules {
			rules = append(rules, ingressRule)
		}
		profile.Spec.IngressRules = rules
	} else if action == "deny" {
		rules := []api.Rule{rule}
		for _, egressRule := range profile.Spec.EgressRules {
			rules = append(rules, egressRule)
		}
		profile.Spec.EgressRules = rules
	} else {
		log.Fatal("action " + action + " is not allow or deny")
	}
	_, err = s.calico.Profiles().Apply(profile)
	if err != nil {
		log.Fatal(err)
	}
	return true
}

func (s *Server) ApplyCalicoProfile(virtualIpItem *VirtualIpItem) bool {
	profile, err := s.calico.Profiles().Get(api.ProfileMetadata{Name: virtualIpItem.appName})
	if err != nil {
		log.Fatal(err)
	}
	rules := []api.Rule{}
	for _, v := range virtualIpItem.ports {
		port, err := strconv.ParseUint(v.port, 10, 16)
		if err != nil {
			log.Fatal(err)
		}
		rule := api.Rule{}
		rule.Action = "allow"
		proto := numorstring.ProtocolFromString(v.proto)
		rule.Protocol = &proto
		rule.Destination.Ports = []numorstring.Port{numorstring.SinglePort(uint16(port))}
		rules = append(rules, rule)
	}
	profile.Spec.IngressRules = rules
	_, err = s.calico.Profiles().Apply(profile)
	if err != nil {
		log.Fatal(err)
	}
	return true
}
