package acl

import (
	"github.com/Sirupsen/logrus"
	"github.com/laincloud/networkd/util"
	"strings"
)

func (self *Acl) removeOldRules(oldRules []*ipTablesRule) {
	if oldRules != nil {
		for _, oldRule := range oldRules {
			if err := self.delRule(oldRule.args...); err != nil {
				self.log.WithFields(logrus.Fields{
					"err": err,
				}).Error("Delete Old WhiteList Ip Rule error")
			}
		}
	}
}

func (self *Acl) fetchTargetIptablesLines(target string) ([]string, error) {
	cmdName := "/bin/sh"
	cmdArgs := "iptables -L INPUT -v -n --line-numbers | grep " + target + "| awk '{print $1}'"
	res, err := util.ExecCommand(cmdName, "-c", cmdArgs)
	if err != nil {
		self.log.WithFields(logrus.Fields{
			"err":    err,
			"target": target,
			"res":    res,
		}).Error("Fetch Target Rules error")
		return nil, err
	}
	res = strings.TrimRight(res, "\n")
	return strings.Split(res, "\n"), nil
}
