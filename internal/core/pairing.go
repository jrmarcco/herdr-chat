package core

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"time"
)

// 配对：
// 把 bot 交给它的 owner。`herdrchat pair` 生成一个码写进 state.pairing，
// owner 在直聊里把它发给 bot，bot 就认下这个人。

// 配对码的规矩。
const (
	PairMinutes = 15
	PairTries   = 5
	PairDigits  = 6
)

// PairWindow 是一个配对码有效的时长。
const PairWindow = PairMinutes * time.Minute

// NewPairingCode 生成一个 6 位配对码 ( crypto/rand；前导零保留 )。
func NewPairingCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", PairDigits, n.Int64()), nil
}
