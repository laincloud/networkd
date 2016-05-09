package client

import (
	"encoding/json"
	lainlet "github.com/laincloud/lainlet/client"
	"net"
)

type JSONVirtualIpPortConfig struct {
	App      string `json:"app"`
	Proc     string `json:"proc"`
	Port     string `json:"port"`
	Proto    string `json:"proto"`
	ProcType string `json:"proctype"`
}
type JSONVirtualIpPortConfigs map[string]JSONVirtualIpPortConfig
type JSONVirtualIpConfigs map[string]JSONVirtualIpPortConfigs

const Version = "0.0.2"
const LainLetVirtualIpKey = "vips"

func Get(endpoint string, appname string) (ip string, err error) {
	client := lainlet.New(endpoint)
	response, err := client.Get("/v2/configwatcher?target=vips", 0)
	if err != nil {
		return "", err
	}
	var vips interface{}
	err = json.Unmarshal(response, &vips)
	keyPrefixLength := len(LainLetVirtualIpKey) + 1
	for key, value := range vips.(map[string]interface{}) {
		virtualIpKey := key[keyPrefixLength:]
		// TODO(xutao) process locked ip
		paresedIp := net.ParseIP(virtualIpKey)
		if paresedIp.To4() == nil {
			continue
		}
		var portConfigs JSONVirtualIpPortConfigs
		err = json.Unmarshal([]byte(value.(string)), &portConfigs)
		if err != nil {
			// TODO(xutao) error log
			continue
		}
		for _, config := range portConfigs {
			if config.App == appname {
				return virtualIpKey, nil
			}
		}
	}
	return "", nil
}
