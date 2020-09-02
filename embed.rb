#!/usr/bin/env ruby

from_file = ARGV[0]
var_name = ARGV[1]
to_file = ARGV[2]

from = File.open(from_file)
to   = File.open(to_file, File::WRONLY|File::CREAT|File::TRUNC)

content = from.read()

to.write(<<EOF
package main

var #{var_name} = []byte{#{content.each_byte.to_a.join(", ")}}
EOF
        )
