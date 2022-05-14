#!/bin/bash

set -eu

SERVER="$1"

make
#scp numblr $SERVER:
rsync -v --progress numblr $SERVER:
ssh $SERVER sudo systemctl stop numblr
ssh $SERVER sudo cp numblr /srv/numblr
ssh $SERVER sudo systemctl restart numblr
ssh $SERVER journalctl -u numblr -f
