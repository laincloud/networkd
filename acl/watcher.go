package acl

import (
	"encoding/json"
	"fmt"
	"github.com/Sirupsen/logrus"
	"github.com/laincloud/networkd/util"
)

const EtcdWhiteListIpsKeyPrefix = "whitelist_ips"
const EtcdWLPortsInKeyPrefix = "whitelist_in_ports"
const EtcdWLPortsExKeyPrefix = "whitelist_ex_ports"
const EtcdPrefixKey = "/lain/config"

func (self *Acl) watchWhiteListIps(watchCh <-chan struct{}) {
	util.WatchConfig(self.log, self.lainlet, EtcdWhiteListIpsKeyPrefix, watchCh, func(datas interface{}) {
		var wlIps []string
		for key, value := range datas.(map[string]interface{}) {
			self.log.WithFields(logrus.Fields{
				"key":     key,
				"domains": value,
			}).Debug("Get white list ips from lainlet")
			var ips []string
			err := json.Unmarshal([]byte(value.(string)), &ips)
			if err != nil {
				self.log.WithFields(logrus.Fields{
					"key":    fmt.Sprintf("/lain/config/%s/%s", EtcdWhiteListIpsKeyPrefix, key),
					"reason": err,
				}).Error("Cannot parse white list ips config")
				continue
			}
			wlIps = append(wlIps, ips...)
		}
		self.wlIps = wlIps
		self.wlipCh <- 1
	})
}

func (self *Acl) watchExWhiteListPorts(watchCh <-chan struct{}) {
	keyPrefixLength := len(EtcdWLPortsExKeyPrefix) + 1
	util.WatchConfig(self.log, self.lainlet, EtcdWLPortsExKeyPrefix, watchCh, func(datas interface{}) {
		exPts := make(map[string][]string)
		for key, value := range datas.(map[string]interface{}) {
			proto := key[keyPrefixLength:]
			self.log.WithFields(logrus.Fields{
				"key":     key,
				"domains": value,
			}).Debug("Get white list external ports from lainlet")
			var ips []string
			err := json.Unmarshal([]byte(value.(string)), &ips)
			if err != nil {
				self.log.WithFields(logrus.Fields{
					"key":    fmt.Sprintf("/lain/config/%s/%s", EtcdWLPortsExKeyPrefix, key),
					"reason": err,
				}).Error("Cannot parse white list external ports config")
				continue
			}
			exPts[proto] = ips
		}
		self.wlExPorts = exPts
		self.exptCh <- 1
	})
}

func (self *Acl) watchInWhiteListPorts(watchCh <-chan struct{}) {
	keyPrefixLength := len(EtcdWLPortsInKeyPrefix) + 1
	util.WatchConfig(self.log, self.lainlet, EtcdWLPortsInKeyPrefix, watchCh, func(datas interface{}) {
		inPts := make(map[string]*protoPorts)
		for key, value := range datas.(map[string]interface{}) {
			ip := key[keyPrefixLength:]
			var ips protoPorts
			err := json.Unmarshal([]byte(value.(string)), &ips)
			if err != nil {
				self.log.WithFields(logrus.Fields{
					"ips":    ips,
					"reason": err,
				}).Error("Cannot parse white list internal ip port config")
				continue
			}
			inPts[ip] = &ips
		}
		self.wlInPorts = inPts
		self.inptCh <- 1
	})
}
