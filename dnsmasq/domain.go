package dnsmasq

import (
	"encoding/json"
	"fmt"
	"github.com/Sirupsen/logrus"
)

const EtcdDnsExtraPrefixKey = "extra_domains"
const EtcdVipPrefixKey = "vip"

func (self *Server) WatchDnsmasqExtra(watchCh <-chan struct{}) {
	self.watchConfig(EtcdDnsExtraPrefixKey, watchCh, func(datas interface{}) {
		var extras []AddressItem
		ip := self.FetchVip()
		for key, value := range datas.(map[string]interface{}) {
			self.log.WithFields(logrus.Fields{
				"key":     key,
				"domains": value,
			}).Debug("Get domain from lainlet")
			var domains []string
			err := json.Unmarshal([]byte(value.(string)), &domains)
			if err != nil {
				self.log.WithFields(logrus.Fields{
					"key":    fmt.Sprintf("/lain/config/%s/%s", EtcdServerPrefixKey, key),
					"reason": err,
				}).Error("Cannot parse domain server config")
				continue
			}
			for _, domain := range domains {
				extras = append(extras, AddressItem{
					ip:     ip,
					domain: domain,
				})
			}
		}
		self.extras = extras
		self.cnfEvCh <- 1
	})
}

func (self *Server) WatchVip(watchCh <-chan struct{}) {
	self.watchConfig(EtcdVipPrefixKey, watchCh, func(datas interface{}) {
		if len(datas.(map[string]interface{})) == 0 {
			self.vip = ""
		} else {
			for key, value := range datas.(map[string]interface{}) {
				self.log.WithFields(logrus.Fields{
					"key": key,
					"vip": value,
				}).Debug("Get vip from lainlet")
				self.vip = value.(string)
			}
		}
		ip := self.FetchVip()
		for i, _ := range self.extras {
			self.extras[i].ip = ip
		}
		self.cnfEvCh <- 1
	})
}

func (self *Server) FetchVip() string {
	ip := self.vip
	if ip == "" || self.vip == "0.0.0.0" {
		ip = self.ip
	}
	return ip
}
