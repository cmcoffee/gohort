package temptool

import "testing"

// curl and wget are not refused at authoring: the sandbox's are stand-ins that
// send through the fetch hook, so a script calling them works at dispatch as
// it did in the workspace shell where the author tried it. Python's network
// libraries still cannot work there and are still refused.
func TestAuthoringRefusesPythonNetworkingButNotCurl(t *testing.T) {
	for _, script := range []string{
		"#!/bin/sh\ncurl -fsSL https://api.github.com/repos/o/r | jq .name\n",
		"#!/bin/sh\nwget -qO- https://example.com/a.txt\n",
		"import subprocess\nsubprocess.run([\"curl\", \"-s\", url])\n",
		"import os\nos.system('wget -q ' + url)\n",
	} {
		if got := detectForbiddenNetworkPatterns(script); got != "" {
			t.Errorf("refused %q as %s", script, got)
		}
	}
	for script, want := range map[string]string{
		"import requests\nrequests.get(url)\n":               "import requests",
		"from urllib.request import urlopen\nurlopen(url)\n": "urllib.request",
		"import socket\nsocket.create_connection((h, 80))\n": "socket.create_connection",
	} {
		if got := detectForbiddenNetworkPatterns(script); got != want {
			t.Errorf("%q: refused as %q, want %q", script, got, want)
		}
	}
}
