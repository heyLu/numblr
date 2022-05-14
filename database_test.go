package main

import (
	"fmt"
	"net"
	"testing"
)

func TestIsTimeoutError(t *testing.T) {
	testCases := []struct {
		err                error
		expectTimeoutError bool
	}{
		{&net.DNSError{IsTimeout: true}, true},
		{fmt.Errorf("random error"), false},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%#v", tc.err), func(t *testing.T) {
			isTimeout := isTimeoutError(tc.err)
			if tc.expectTimeoutError != isTimeout {
				t.Errorf("expected to be %v but was %v", tc.expectTimeoutError, isTimeout)
			}
		})
	}
}
