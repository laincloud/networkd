package server

import (
	"net"
	"strings"
	"bufio"
	"sync"
	"bytes"
	"fmt"
	"github.com/deckarep/golang-set"
)

type LocalData struct {
	// record in hosts file, will match the domain exactly
	hosts         map[string][]string
	// records in address file, will match all the subdomains
	wildcardHosts *suffixTreeNode
	mu            sync.RWMutex
}

func NewLocalData() *LocalData {
	d := &LocalData{
		hosts:         make(map[string][]string),
		wildcardHosts: newSuffixTreeRoot(),
	}
	return d
}

func (d *LocalData) DumpConfig() []byte {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var data []byte
	literal := "# exact domains\n"
	data = append(data, literal...)
	for domain, ips := range d.hosts {
		content := fmt.Sprintf("%s %v\n", domain, ips)
		data = append(data, content...)
	}
	literal = "\n# wildcard domains\n"
	data = append(data, literal...)
	d.wildcardHosts.iterFunc(nil, func(keys []string, v mapset.Set) {
		//reverse keys from suffixTreeNode, note maybe len(keys) == 1.
		reversedKeys := make([]string, len(keys))
		for i, j := 0, len(keys) - 1; i <= j; i, j = i+1, j-1 {
			reversedKeys[i], reversedKeys[j] = keys[j], keys[i]
		}

		domain := strings.Join(reversedKeys, ".")
		for val := range v.Iter() {
			content := fmt.Sprintf("address=/%s/%s\n", domain, val.(string))
			data = append(data, content...)
		}
	})
	return data
}
func (d *LocalData) Get(domain string) ([]string, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	domain = strings.ToLower(domain)
	domain = strings.TrimSuffix(domain, ".")
	ips, ok := d.hosts[domain]
	if ok {
		return ips, true
	}

	queryKeys := strings.Split(domain, ".")

	if v, found := d.wildcardHosts.search(queryKeys); found {
		glog.Debugf("%s be found in address list, ips: %v", domain, v)
		return setToStringSlice(v), true
	}
	return nil, false
}

func (d *LocalData) ReplaceWildcardHostsWithBytes(buf []byte) {
	hosts := parseAddressList(buf)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.wildcardHosts = hosts
}

func (d *LocalData) ReplaceHosts(hosts map[string][]string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.hosts = hosts
}

func (d *LocalData) ReplaceWildcardHosts(data map[string][]string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	hosts := newSuffixTreeRoot()
	for domain, v := range data {
			domain = strings.TrimPrefix(domain, ".")
			domain = strings.TrimPrefix(domain, "*.")
			domain = strings.ToLower(domain)

			for _, ip := range v {
				if !isDomain(domain) || !isIP(ip) {
					continue
				}
				hosts.sinsert(strings.Split(domain, "."), ip)
			}
	}
	d.wildcardHosts = hosts
}

func (h *LocalData) GetIpList(domain string, family int) ([]net.IP, bool) {
	var sips []string
	var ip net.IP
	var ips []net.IP

	sips, _ = h.Get(domain)

	if sips == nil {
		return nil, false
	}

	for _, sip := range sips {
		switch family {
		case _IP4Query:
			ip = net.ParseIP(sip).To4()
		case _IP6Query:
			ip = net.ParseIP(sip).To16()
		default:
			continue
		}
		if ip != nil {
			ips = append(ips, ip)
		}
	}

	return ips, ips != nil
}

func parseAddressList(buf []byte) *suffixTreeNode {
	hosts := newSuffixTreeRoot()
	scanner := bufio.NewScanner(bytes.NewReader(buf))
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)

		if !strings.HasPrefix(line, "address") {
			continue
		}

		sli := strings.Split(line, "=")
		if len(sli) != 2 {
			continue
		}

		line = strings.TrimSpace(sli[1])

		tokens := strings.Split(line, "/")
		switch len(tokens) {
		case 3:
			domain := tokens[1]
			domain = strings.TrimPrefix(domain, ".")
			domain = strings.TrimPrefix(domain, "*.")
			domain = strings.ToLower(domain)

			ip := tokens[2]

			if !isDomain(domain) || !isIP(ip) {
				continue
			}
			hosts.sinsert(strings.Split(domain, "."), ip)
		}
	}
	return hosts
}

func parseHosts(buf []byte) map[string]string {
	hosts := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(buf))
	for scanner.Scan() {

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
		if !isIP(ip) {
			continue
		}

		// Would have multiple columns of domain in line.
		// Such as "127.0.0.1  localhost localhost.domain" on linux.
		// The domains may not strict standard, like "local" so don't check with f.isDomain(domain).
		for i := 1; i <= len(sli)-1; i++ {
			domain := strings.TrimSpace(sli[i])
			if domain == "" {
				continue
			}

			hosts[strings.ToLower(domain)] = ip
		}
	}
	return hosts
}
