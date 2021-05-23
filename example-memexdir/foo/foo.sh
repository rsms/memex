#!/bin/sh
echo "hello from foo"
trap exit SIGINT

echo "args: $@"
echo "env FOO=$FOO"
echo "env BAR=$BAR"

# env | sort
# exec sleep 30

for i in `seq 3 1`; do
  echo "$i"
  sleep 1
done
