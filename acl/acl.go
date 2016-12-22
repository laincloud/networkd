package acl

import (
	"errors"
	"github.com/Sirupsen/logrus"
	lainlet "github.com/laincloud/lainlet/client"
	"github.com/laincloud/networkd/util"
	"strings"
)

const IptablesNotFound = "iptables: No chain/target/match by that name.\n"
const IptablesChainRulesFound = "iptables: Bad rule (does a matching rule exist in that chain?).\n"
const IptablesLocked = "Another app is currently holding the xtables lock.\n"
const IptablesChainFound = "iptables: Chain already exists.\n"
const IptablesRuleFound = "iptables: Rule already exists.\n"

const CMD_IPTABLES = "iptables"

const (
	EntryChain = "lain-ENTRY" //allow anywaher enter explored port
	InputChain = "lain-INPUT" //allow white list ip and port enter lain
)

var (
	invalidRuls     = []string{"INPUT", "-m", "state", "--state", "INVALID", "-j", "DROP"}
	establishedRuls = []string{"INPUT", "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT"}
	loRuls          = []string{"INPUT", "-i", "lo", "-j", "ACCEPT"}
	dockerRuls      = []string{"INPUT", "-i", "docker0", "-j", "ACCEPT"}
	caliRuls        = []string{"INPUT", "-i", "cali+", "-j", "ACCEPT"}
	sshRules        = []string{"INPUT", "-p", "tcp", "--dport", "22", "-j", "ACCEPT"}
)

type ipTablesRule struct {
	args []string
}

type protoPorts struct {
	Ports map[string][]string `json:"ports"` // proto => ports ie: {tcp:["11","22","33"]}
}

/**
 * white ips can be singe ip like 127.0.0.1
 * can be range ips like 127.0.0.1-127.0.0.3
 * can be ip segment like 192.168.0.0/16
 */
type Acl struct {
	lainlet    *lainlet.Client
	log        *logrus.Logger
	isRunning  bool
	stopCh     chan struct{}
	wlipCh     chan int
	exptCh     chan int
	inptCh     chan int
	wlIps      []string
	wlExPorts  map[string][]string    // proto => ports
	wlInPorts  map[string]*protoPorts //ip => protoPorts
	oldWlRule  *ipTablesRule
	oldExRules []*ipTablesRule
	oldInRules []*ipTablesRule
}

func New(log *logrus.Logger, lainlet *lainlet.Client) *Acl {
	return &Acl{
		log:        log,
		lainlet:    lainlet,
		isRunning:  false,
		stopCh:     make(chan struct{}),
		wlipCh:     make(chan int),
		exptCh:     make(chan int),
		inptCh:     make(chan int),
		oldExRules: make([]*ipTablesRule, 0),
		oldInRules: make([]*ipTablesRule, 0),
	}
}

func (self *Acl) newChain(chain string) {
	cmdName := CMD_IPTABLES
	cmdArgs := []string{"-N", chain}
	if res, err := util.ExecCommand(cmdName, cmdArgs...); err != nil {
		self.log.WithFields(logrus.Fields{
			"err":   err,
			"res":   res,
			"chain": chain,
		}).Error("New Chain error")
	}
}

func (self *Acl) delChain(chain string) {
	cmdName := CMD_IPTABLES
	cmdArgs := []string{"-X", chain}
	if res, err := util.ExecCommand(cmdName, cmdArgs...); err != nil {
		self.log.WithFields(logrus.Fields{
			"err":   err,
			"res":   res,
			"chain": chain,
		}).Error("New Chain error")
	}
}

func (self *Acl) chainPolicy(chain string, policy string) {
	cmdName := CMD_IPTABLES
	cmdArgs := []string{"-P", chain, policy}
	if _, err := util.ExecCommand(cmdName, cmdArgs...); err != nil {
		self.log.WithFields(logrus.Fields{
			"err": err,
		}).Error("policy Chain error")
	}
}

func (self *Acl) checkRule(ruleArgs ...string) (string, error) {
	cmdName := CMD_IPTABLES
	cmdArgs := []string{"-C"}
	cmdArgs = append(cmdArgs, ruleArgs...)
	return util.ExecCommand(cmdName, cmdArgs...)
}

func (self *Acl) addRule(ruleArgs ...string) error {
	if res, err := self.checkRule(ruleArgs...); err != nil {
		if res != IptablesNotFound && res != IptablesChainRulesFound {
			self.log.WithFields(logrus.Fields{
				"err":      res,
				"ruleArgs": ruleArgs,
			}).Error("check rule error")
			return err
		}
	} else {
		return errors.New(IptablesRuleFound)
	}
	cmdName := CMD_IPTABLES
	cmdArgs := []string{"-A"}
	cmdArgs = append(cmdArgs, ruleArgs...)
	res, err := util.ExecCommand(cmdName, cmdArgs...)
	if err != nil {
		self.log.WithFields(logrus.Fields{
			"err":     err,
			"res":     res,
			"cmdArgs": cmdArgs,
		}).Error("add rule error")
	}
	return err
}

func (self *Acl) delRule(ruleArgs ...string) error {
	cmdName := CMD_IPTABLES
	cmdArgs := []string{"-D"}
	cmdArgs = append(cmdArgs, ruleArgs...)
	_, err := util.ExecCommand(cmdName, cmdArgs...)
	return err
}

func (self *Acl) clearTargetRule(chain string) {
	lines, err := self.fetchTargetIptablesLines(chain)
	if err != nil {
		return
	}
	//delete rule from end
	n := len(lines) - 1
	for i, _ := range lines {
		line := lines[n-i]
		self.delRule("INPUT", line)
	}
}

func (self *Acl) clearTargetRules() {
	self.clearTargetRule(EntryChain)
	self.clearTargetRule(InputChain)
}

func (self *Acl) flushChain(chain string) error {
	cmdName := CMD_IPTABLES
	cmdArgs := []string{"-F", chain}
	_, err := util.ExecCommand(cmdName, cmdArgs...)
	return err
}

func (self *Acl) newChains() {
	self.newChain(EntryChain)
	self.newChain(InputChain)
}

func (self *Acl) delChains() {
	self.flushChain(InputChain)
	self.flushChain(EntryChain)
	self.delChain(InputChain)
	self.delChain(EntryChain)
}

func (self *Acl) initChain() {
	self.flushChain(EntryChain)
	self.flushChain(InputChain)
	entryRules := []string{EntryChain, "-j", "ACCEPT"}
	self.addRule(entryRules...)
}

func (self *Acl) cleanConfigRules() {
	if self.oldWlRule != nil {
		self.delRule(self.oldWlRule.args...)
	}
	self.removeOldRules(self.oldExRules)
	self.removeOldRules(self.oldInRules)
}

func (self *Acl) initRules() {
	self.addRule(loRuls...)
	self.addRule(dockerRuls...)
	self.addRule(caliRuls...)
	self.addRule(invalidRuls...)
	self.addRule(establishedRuls...)
	self.addRule(sshRules...)
}

func (self *Acl) cleanInitRules() {
	self.delRule(loRuls...)
	self.delRule(dockerRuls...)
	self.delRule(caliRuls...)
	self.delRule(invalidRuls...)
	self.delRule(establishedRuls...)
	self.delRule(sshRules...)
}

func (self *Acl) init() {
	self.initRules()
	self.clearTargetRules()
	self.newChains()
	self.initChain()
	self.chainPolicy("INPUT", "DROP")
}

func (self *Acl) cleanAllRules() {
	self.chainPolicy("INPUT", "ACCEPT")
	self.cleanConfigRules()
	self.cleanInitRules()
	self.delChains()
}

func (self *Acl) RunAcl() {
	self.isRunning = true
	self.init()
	stopWhiteIpsCh := make(chan struct{})
	defer close(stopWhiteIpsCh)
	stopInWhitePortsCh := make(chan struct{})
	defer close(stopInWhitePortsCh)
	stopExWhitePortsCh := make(chan struct{})
	defer close(stopExWhitePortsCh)
	go self.watchWhiteListIps(stopWhiteIpsCh)
	go self.watchInWhiteListPorts(stopInWhitePortsCh)
	go self.watchExWhiteListPorts(stopExWhitePortsCh)
	for {
		select {
		case <-self.wlipCh:
			self.log.Debug("Received Acl White list event")
			self.reconfigWhiteListIps()
		case <-self.exptCh:
			self.log.Debug("Received Acl External Ports event")
			self.reconfigExWhiteListPorts()
		case <-self.inptCh:
			self.log.Debug("Received Acl Internal Ports event")
			self.reconfigInWhiteListPorts()
		case <-self.stopCh:
			self.isRunning = false
			stopWhiteIpsCh <- struct{}{}
			stopInWhitePortsCh <- struct{}{}
			stopExWhitePortsCh <- struct{}{}
			return
		}
	}
}

func (self *Acl) StopAcl() {
	if self.isRunning {
		self.stopCh <- struct{}{}
	}
	self.isRunning = false
	self.cleanAllRules()
	self.log.Error("StopAcl over")
}

func (self *Acl) reconfigWhiteListIps() {
	if self.oldWlRule != nil {
		self.delRule(self.oldWlRule.args...)
	}
	if len(self.wlIps) == 0 {
		return
	}
	ips := strings.Join(self.wlIps, ",")
	ruleArgs := []string{"INPUT", "-s", ips, "-j", EntryChain}
	if err := self.addRule(ruleArgs...); err == nil {
		self.oldWlRule = &ipTablesRule{args: ruleArgs}
	} else {
		self.log.WithFields(logrus.Fields{
			"err": err,
		}).Error("Add New WhiteList Ips Rule error")
	}
}

func (self *Acl) reconfigExWhiteListPorts() {
	self.removeOldRules(self.oldExRules)
	self.oldExRules = self.oldExRules[0:0]
	for proto, ports := range self.wlExPorts {
		if len(ports) == 0 {
			continue
		}
		portstr := strings.Join(ports, ",")
		ruleArgs := []string{"INPUT", "-p", proto, "-m", "multiport", "--dport", portstr, "-j", EntryChain}
		if err := self.addRule(ruleArgs...); err == nil {
			self.oldExRules = append(self.oldExRules, &ipTablesRule{args: ruleArgs})
		} else {
			self.log.WithFields(logrus.Fields{
				"err": err,
			}).Error("Add New WhiteList External Ports Rule error")
		}
	}
}

func (self *Acl) reconfigInWhiteListPorts() {
	self.removeOldRules(self.oldInRules)
	self.flushChain(InputChain)
	self.oldInRules = self.oldInRules[0:0]
	for ip, protoPorts := range self.wlInPorts {
		if len(protoPorts.Ports) == 0 {
			continue
		}
		ipArgs := []string{"INPUT", "-s", ip, "-j", InputChain}
		if strings.Contains(ip, "-") {
			ipArgs = []string{"INPUT", "-m", "iprange", "--src-range", ip, "-j", InputChain}
		}
		if err := self.addRule(ipArgs...); err == nil {
			self.oldInRules = append(self.oldInRules, &ipTablesRule{args: ipArgs})
		} else {
			self.log.WithFields(logrus.Fields{
				"err": err,
			}).Error("Add New Internal WhiteList Ip Port Chain Rule error")
		}
		for proto, ports := range protoPorts.Ports {
			portstr := strings.Join(ports, ",")
			portArgs := []string{"-p", proto, "-m", "multiport", "--dport", portstr, "-j", "ACCEPT"}
			ruleArgs := append([]string{InputChain}, ipArgs[1:len(ipArgs)-2]...)
			ruleArgs = append(ruleArgs, portArgs...)
			if err := self.addRule(ruleArgs...); err != nil {
				self.log.WithFields(logrus.Fields{
					"err": err,
				}).Error("Add New WhiteList Internal Ports Rule error")
			}
		}
	}
}
