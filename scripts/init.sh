etcdctl set /lain/config/vips/192.168.11.254 '{}'
etcdctl set /lain/config/vips/192.168.12.254 ''
etcdctl set /lain/config/vips/192.168.12.254.lock 'node2'
etcdctl set /lain/config/vips/192.168.77.201 '{"app": "webrouter", "proc": "worker", "ports": [{"src": "80", "proto": "tcp"}, {"src": "443", "proto": "tcp"}], "excluded_nodes": ["node4"]}'
etcdctl set /lain/config/vips/192.168.77.202 '{"app": "tinydns", "proc": "worker", "ports": [{"src": "5555", "proto": "udp", "dest": "53"}], "excluded_nodes": ["node4"]}'
etcdctl set /lain/config/vips/192.168.77.204 '{"app": "hello", "proc": "web", "ports": [{"src": "80", "proto": "tcp"}]}'

etcdctl set /lain/config/vips/0.0.0.0:5555 '{"app": "tinydns", "proc": "worker", "ports": [{"dest": "53", "proto": "udp"}]}'
etcdctl set /lain/config/vips/0.0.0.0:80 '{"app": "webrouter", "proc": "worker", "ports": [{"dest": "80", "proto": "tcp"}]}'
etcdctl set /lain/config/vips/0.0.0.0:443 '{"app": "webrouter", "proc": "worker", "ports": [{"dest": "443", "proto": "tcp"}]}'
