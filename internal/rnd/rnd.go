// Package rnd makes the random text that the other packages need: session
// tokens, the player secret and the name of a staging directory.
//
// It is one function with one failure rule. Four places made their own copy
// before this, and each one answered a failed read differently: one panicked, one
// ignored the error and one wrote a value of zeros. A secret of zeros is a secret
// that anybody can guess, so the rule is the strict one for every caller.
package rnd

import (
	"crypto/rand"
	"encoding/hex"
)

// Hex gives n random bytes as hexadecimal text, so the answer is 2n characters
// long.
//
// It panics when the system gives no random bytes. crypto/rand does not fail on
// any system that we run on: on Linux it reads getrandom(2), which answers after
// the pool is ready. A caller that went on with a weak value would open a door
// with no lock, and a daemon that stops says so in the log.
func Hex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("rnd: the system gave no random bytes: " + err.Error())
	}
	return hex.EncodeToString(b)
}
