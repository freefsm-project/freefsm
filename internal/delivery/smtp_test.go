package delivery

import (
	"errors"
	"net"
	"net/textproto"
	"testing"
)

func TestClassifySMTPError(t *testing.T) {
	tests := []struct {
		err  error
		kind SendErrorKind
	}{
		{&textproto.Error{Code: 550, Msg: "rejected"}, SendPermanent},
		{&textproto.Error{Code: 451, Msg: "later"}, SendTemporary},
		{errors.New("final DATA acceptance: connection lost"), SendAmbiguous},
		{&net.DNSError{IsTimeout: true}, SendTemporary},
		{errors.New("STARTTLS required but unavailable"), SendPermanent},
	}
	for _, tt := range tests {
		var got *SendError
		if err := classifySMTPError(tt.err); !errors.As(err, &got) || got.Kind != tt.kind {
			t.Fatalf("classify %v = %#v", tt.err, err)
		}
	}
}
