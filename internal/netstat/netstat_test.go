package netstat

import (
	"strings"
	"testing"
)

const tcp = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 9354125 1 0000000000000000 100 0 0 10 0
   1: 0100007F:20D0 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1002        0 5259 1 0000000000000000 100 0 0 10 0
   2: 0100007F:20D0 0100007F:C350 01 00000000:00000000 00:00000000 00000000  1002        0 0 1 0000000000000000 100 0 0 10 0
`

const tcp6 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:18EB 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000   111        0 11720304 1 0000000000000000 100 0 0 10 0
   1: 00000000000000000000000000000000:0016 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 5265 1 0000000000000000 100 0 0 10 0
`

func TestParse(t *testing.T) {
	ls, err := Parse(strings.NewReader(tcp))
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 2 {
		t.Fatalf("want 2 listeners (established skipped), got %+v", ls)
	}
	if ls[0].Port != 80 || ls[0].IP.String() != "0.0.0.0" || ls[0].Loopback() {
		t.Fatalf("public 80: %+v", ls[0])
	}
	if ls[1].Port != 8400 || ls[1].IP.String() != "127.0.0.1" || !ls[1].Loopback() || ls[1].UID != 1002 {
		t.Fatalf("loopback 8400: %+v", ls[1])
	}
	ls6, err := Parse(strings.NewReader(tcp6))
	if err != nil {
		t.Fatal(err)
	}
	if ls6[0].IP.String() != "::1" || ls6[0].Port != 6379 || !ls6[0].Loopback() {
		t.Fatalf("::1 6379: %+v", ls6[0])
	}
	if ls6[1].IP.String() != "::" || ls6[1].Port != 22 || ls6[1].Loopback() {
		t.Fatalf(":: 22: %+v", ls6[1])
	}
}
