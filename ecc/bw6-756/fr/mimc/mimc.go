// Copyright 2020 ConsenSys Software Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Code generated by consensys/gnark-crypto DO NOT EDIT

package mimc

import (
	"errors"
	"hash"

	"github.com/consensys/gnark-crypto/ecc/bw6-756/fr"
	"golang.org/x/crypto/sha3"
	"math/big"
	"sync"
)

const (
	mimcNbRounds = 91
	seed         = "seed"   // seed to derive the constants
	BlockSize    = fr.Bytes // BlockSize size that mimc consumes
)

// Params constants for the mimc hash function
var (
	mimcConstants [mimcNbRounds]fr.Element
	once          sync.Once
)

// digest represents the partial evaluation of the checksum
// along with the params of the mimc function
type digest struct {
	h    fr.Element
	data []byte // data to hash
}

// GetConstants exposed to be used in gnark
func GetConstants() []big.Int {
	once.Do(initConstants) // init constants
	res := make([]big.Int, mimcNbRounds)
	for i := 0; i < mimcNbRounds; i++ {
		mimcConstants[i].ToBigIntRegular(&res[i])
	}
	return res
}

// NewMiMC returns a MiMCImpl object, pure-go reference implementation
func NewMiMC() hash.Hash {
	d := new(digest)
	d.Reset()
	return d
}

// Reset resets the Hash to its initial state.
func (d *digest) Reset() {
	d.data = nil
	d.h = fr.Element{0, 0, 0, 0}
}

// Sum appends the current hash to b and returns the resulting slice.
// It does not change the underlying hash state.
func (d *digest) Sum(b []byte) []byte {
	buffer := d.checksum()
	d.data = nil // flush the data already hashed
	hash := buffer.Bytes()
	b = append(b, hash[:]...)
	return b
}

// BlockSize returns the hash's underlying block size.
// The Write method must be able to accept any amount
// of data, but it may operate more efficiently if all writes
// are a multiple of the block size.
func (d *digest) Size() int {
	return BlockSize
}

// BlockSize returns the number of bytes Sum will return.
func (d *digest) BlockSize() int {
	return BlockSize
}

// Write (via the embedded io.Writer interface) adds more data to the running hash.
//
// Each []byte block of size BlockSize represents a big endian fr.Element.
//
// If len(p) is not a multiple of BlockSize and any of the []byte in p represent an integer
// larger than fr.Modulus, this function returns an error.
//
// To hash arbitrary data ([]byte not representing canonical field elements) use Decompose
// function in this package.
func (d *digest) Write(p []byte) (n int, err error) {
	n = len(p)
	if n%BlockSize != 0 {
		return 0, errors.New("invalid input length: must represent a list of field elements, expects a []byte of len m*BlockSize")
	}

	// ensure each block represents a field element in canonical reduced form
	for i := 0; i < n; i += BlockSize {
		if _, err = fr.BigEndian.Element((*[BlockSize]byte)(p[i : i+BlockSize])); err != nil {
			return 0, err
		}
	}

	d.data = append(d.data, p...)
	return
}

// Hash hash using Miyaguchi-Preneel:
// https://en.wikipedia.org/wiki/One-way_compression_function
// The XOR operation is replaced by field addition, data is in Montgomery form
func (d *digest) checksum() fr.Element {
	// Write guarantees len(data) % BlockSize == 0

	// TODO @ThomasPiellard shouldn't Sum() returns an error if there is no data?
	if len(d.data) == 0 {
		d.data = make([]byte, BlockSize)
	}

	for i := 0; i < len(d.data); i += BlockSize {
		x, _ := fr.BigEndian.Element((*[BlockSize]byte)(d.data[i : i+BlockSize]))
		r := d.encrypt(x)
		d.h.Add(&r, &d.h).Add(&d.h, &x)
	}

	return d.h
}

// plain execution of a mimc run
// m: message
// k: encryption key
func (d *digest) encrypt(m fr.Element) fr.Element {
	once.Do(initConstants) // init constants

	for i := 0; i < mimcNbRounds; i++ {
		// m = (m+k+c)^5
		var tmp fr.Element
		tmp.Add(&m, &d.h).Add(&tmp, &mimcConstants[i])
		m.Square(&tmp).
			Square(&m).
			Mul(&m, &tmp)
	}
	m.Add(&m, &d.h)
	return m
}

// Sum computes the mimc hash of msg from seed
func Sum(msg []byte) ([]byte, error) {
	var d digest
	if _, err := d.Write(msg); err != nil {
		return nil, err
	}
	h := d.checksum()
	bytes := h.Bytes()
	return bytes[:], nil
}

func initConstants() {
	bseed := ([]byte)(seed)

	hash := sha3.NewLegacyKeccak256()
	_, _ = hash.Write(bseed)
	rnd := hash.Sum(nil) // pre hash before use
	hash.Reset()
	_, _ = hash.Write(rnd)

	for i := 0; i < mimcNbRounds; i++ {
		rnd = hash.Sum(nil)
		mimcConstants[i].SetBytes(rnd)
		hash.Reset()
		_, _ = hash.Write(rnd)
	}
}
