package godns

import (
	"bufio"
	"strings"
	"os"
	"net"

	"github.com/Sirupsen/logrus"
)

const StaticHostsFile = "/etc/hosts"

func (self *Godns) UpdateStaticHosts() {
	buf, err := os.Open(StaticHostsFile)
	if err != nil {
		glog.Warnf("Update hosts records from file failed %s", err)
		return
	}
	defer buf.Close()

	self.mu.Lock()
	defer self.mu.Unlock()

	self.staticHosts = make(map[string]string)

	for scanner := bufio.NewScanner(buf); scanner.Scan(); {
		line := scanner.Text()
		line = strings.TrimSpace(line)
		line = strings.Replace(line, "\t", " ", -1)

		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}

		sli := strings.Split(line, " ")

		if len(sli) < 2 {
			continue
		}

		ip := sli[0]
		if net.ParseIP(ip) == nil {
			continue
		}

		// Would have multiple columns of domain in line.
		// Such as "127.0.0.1  localhost localhost.domain" on linux.
		for i := 1; i <= len(sli)-1; i++ {
			domain := strings.TrimSpace(sli[i])
			if domain == "" {
				continue
			}

			self.staticHosts[strings.ToLower(domain)] = ip
			glog.WithFields(logrus.Fields{
				"domain": domain,
				"ip":     ip,
			}).Infof("Get domain from %s", StaticHostsFile)
		}
	}
}
