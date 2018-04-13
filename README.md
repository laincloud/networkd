Networkd
========
[![codecov](https://codecov.io/gh/laincloud/networkd/branch/master/graph/badge.svg)](https://codecov.io/gh/laincloud/networkd)

layer 0 network daemon.

## Dependency
- ip (iproute-3.10.0-13.el7.x86_64)
- arping (iputils-20121221-6.el7.x86_64)
- iptables (iptables-1.4.21-13.el7.x86_64)
- docker
- etcd
- lainlet

## Float IP

1. watch etcd/lainlet key: `/lain/config/vips/*`
1. watch procs in `/lain/config/vips/*`
1. config node vip

### virtual ip config protocol

- key: /lain/config/vips/{IP}
- value:

    ```javascript
    {
        "app": "APP1", # lain app name, required
        "proc": "PROC1", # lain app proc name, required
        "ports": [
            {
                "src": "PORT1", # source port (virtual ip/host port), required
                "proto": "tcp", # port protocol, optional, default: `tcp`, options: `tcp`, `udp`
                "dest": "APPPORT1" # destination port (lain app port), optional, default: `PORT1(current virtual ip port)`
            },
            {
                ...
            }
        ], # lain app ports, required
        "excluded_nodes": ["NODE1", "NODE2"] # optional, default: `[]`
    }
    ```

### interface

TBD

PS: only support default interface now

### example

vip: 192.168.10.254

1. lock key
    - key: `/lain/networkd/vips/192.168.10.254.lock`
    - value:`node1`  # node hostname
1. config key
    - key: `/lain/config/vips/192.168.10.254`
    - value: json # ip config

        ```json
        {
            "app": "resource.elb.webrouter",
            "proc": "haproxy",
            "ports": [
                {
                    "src": "80",
                    "proto": "tcp"
                },
                {
                    "src": "443",
                    "proto": "tcp"
                },
                {
                    "src": "5555",
                    "proto": "udp",
                    "dest": "53"
                },
            ]
        }
        ```

## Dns
Networkd contains a embedded dns server similar to dnsmasq.
All resolvable domains from etcd are configured in /lain/config/domains.

1. key
   - exact domain, e.g., `/lain/config/domains/etcd.lain`, `/lain/config/domains/docker.lain`.
   - wildcard domain begins with `*.`, e.g., `/lain/config/domains/*.lain`, `/lain/config/domains/*.lain.local`.
2. value
   - type `` to resolve to the specified IPs, e.g., `{"ips":["10.131.0.72"],"type":""}`.
   - type `node` to resolve to node IP, e.g., `{"ips":[],"type":"node"}`.
   - type `webrouter` to resolve to webrouter IPs, e.g., `{"ips":[],"type":"webrouter"}`.
3. dump dns config
   - `curl http://127.0.0.1:3000/v1/dns/config`

## Tinydns

dynamic dns server conf of tinydns app

## Swarm

1. dynamic dns host conf of swarm manager
    1. `swarm.lain`
2. `/lain/swarm/docker/swarm/leader`

## Deployd

1. dynamic dns host conf of deployd
    1. `deployd.lain`
1. `/lain/deployd/leader`

## Webrouter

1. dynamic `webrouter.lain` when no vip for webrouter

## Resolv.conf

1. Watch /etc/resolv.conf
2. Ensure `nameserver 127.0.0.1` in the first line.
3. synchronize name servers from /etc/resolv.conf
4. Remove `rotate` options

## TODO

1. Split lock & health goroutine
2. Print iptables acl rules

## License

Networkd is released under the [MIT license](LICENSE).
