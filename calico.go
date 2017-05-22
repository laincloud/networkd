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
	for _, item := range profile.Spec.IngressRules {
		if item.Action == action && item.Protocol.StrVal == protocol && len(item.Destination.Ports) == 1 && item.Destination.Ports[0].MaxPort == uint16(p) && item.Destination.Ports[0].MinPort == uint16(p) {
			return true
		}
	}
	rule := api.Rule{}
	rule.Action = action
	proto := numorstring.ProtocolFromString(protocol)
	rule.Protocol = &proto
	rule.Destination.Ports = []numorstring.Port{numorstring.SinglePort(uint16(p))}
	profile.Spec.IngressRules = append(profile.Spec.IngressRules, rule)
	_, err = s.calico.Profiles().Apply(profile)
	if err != nil {
		log.Fatal(err)
	}
	return true
}

func (s *Server) ApplyCalicoProfile(virtualIpItem *VirtualIpItem) bool {
	profile := api.NewProfile()
	profile.Metadata = api.ProfileMetadata{Name: virtualIpItem.appName}
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
		profile.Spec.IngressRules = append(profile.Spec.IngressRules, rule)
	}
	_, err := s.calico.Profiles().Apply(profile)
	if err != nil {
		log.Fatal(err)
	}
	return true
}
