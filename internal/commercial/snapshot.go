package commercial

import (
	"encoding/json"
	"fmt"
	"math/big"
)

// Snapshot is the accepted monetary representation, not a live projection of
// Customer or catalog data. An absent snapshot denotes historical arithmetic.
type Snapshot struct {
	Version int    `json:"version"`
	Input   Input  `json:"input"`
	Result  Result `json:"result"`
}

func Accept(in Input) (string, error) {
	if in.Lines == nil {
		in.Lines = []Line{}
	}
	result, err := Calculate(in)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(Snapshot{Version: 1, Input: in, Result: result})
	return string(b), err
}

func Read(raw string) (Snapshot, error) {
	var s Snapshot
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return s, err
	}
	if s.Version != 1 || len(s.Input.Lines) != len(s.Result.Lines) {
		return s, fmt.Errorf("unsupported commercial financial snapshot")
	}
	return s, nil
}

// FormatCents never passes through floating point, including at int64 limits.
func FormatCents(cents int64) string {
	n := new(big.Int).SetInt64(cents)
	sign := ""
	if n.Sign() < 0 {
		sign = "-"
		n.Abs(n)
	}
	q, r := new(big.Int), new(big.Int)
	q.QuoRem(n, big.NewInt(100), r)
	return fmt.Sprintf("%s%s.%02d", sign, q.String(), r.Int64())
}
