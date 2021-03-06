#!/usr/bin/env bash

# Topo.
#
#     100.141        --        200.130
#	      |                        |
# 192.168.209.141  <=====>   192.168.209.130

sun=192.168.209.130
sun_net=192.168.200.0/24
moon=192.168.209.141
moon_net=192.168.100.0/24

# --
ip xfrm state flush

ip xfrm state add src ${moon} dst ${sun} proto esp spi 0x00000301 mode tunnel auth md5 0x96358c90783bbfa3d7b196ceabe0536b enc des3_ede 0xf6ddb555acfd9d77b03ea3843f2653255afe8eb5573965df
ip xfrm state add src ${sun} dst ${moon} proto esp spi 0x00000302 mode tunnel auth md5 0x99358c90783bbfa3d7b196ceabe0536b enc des3_ede 0xffddb555acfd9d77b03ea3843f2653255afe8eb5573965df

# --
ip xfrm policy flush

ip xfrm policy add src ${moon_net} dst ${sun} dir out ptype main tmpl src ${moon} dst ${sun} proto esp mode tunnel
ip xfrm policy add src ${sun} dst ${moon_net} dir in ptype main tmpl src ${sun} dst ${moon} proto esp mode tunnel
ip xfrm policy add src ${sun} dst ${moon_net} dir fwd ptype main tmpl src ${sun} dst ${moon} proto esp mode tunnel

ip xfrm policy ls
