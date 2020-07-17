// BSD 3-Clause License
//
// Copyright (c) 2020, Sperax
// All rights reserved.
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are met:
//
// 1. Redistributions of source code must retain the above copyright notice, this
//    list of conditions and the following disclaimer.
//
// 2. Redistributions in binary form must reproduce the above copyright notice,
//    this list of conditions and the following disclaimer in the documentation
//    and/or other materials provided with the distribution.
//
// 3. Neither the name of the copyright holder nor the names of its
//    contributors may be used to endorse or promote products derived from
//    this software without specific prior written permission.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
// AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
// IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
// DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE
// FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
// DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
// SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER
// CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY,
// OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
// OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

package bdls_engine

import (
	"crypto/ecdsa"
	"encoding/binary"
	"errors"
	"math/big"

	"github.com/Sperax/SperaxChain/common"
	"github.com/Sperax/SperaxChain/common/hexutil"
	"github.com/Sperax/SperaxChain/consensus"
	"github.com/Sperax/SperaxChain/core/state"
	"github.com/Sperax/SperaxChain/core/types"
	"github.com/Sperax/SperaxChain/crypto"
	"github.com/Sperax/SperaxChain/params"
	"github.com/Sperax/SperaxChain/rlp"
	"golang.org/x/crypto/sha3"
)

var (
	// block 0 common random number
	W0 = crypto.Keccak256Hash(hexutil.MustDecode("0x3243F6A8885A308D313198A2E037073"))
	// potential propser expectation
	E1 = big.NewInt(5)
	// BFT committee expectationA
	E2 = big.NewInt(50)
	// unit of staking SPA
	Alpha = new(big.Int).Mul(big.NewInt(100000), big.NewInt(params.Ether))

	MaxUint256 = big.NewFloat(0).SetInt(big.NewInt(0).SetBytes([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}))

	// transfering tokens to this address will be specially treated
	StakingAddress = common.Address{0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE, 0xEE}
)

var (
	ErrStakingRequest       = errors.New("already staked")
	ErrStakingMinimumTokens = errors.New("staking has less than minimum tokens")
	ErrRedeemRequest        = errors.New("not staked")
)

// types of staking related operation
type StakingOp byte

// Staking Operations
const (
	Staking = StakingOp(0x00)
	Redeem  = StakingOp(0xFF)
)

// StakingRequest will be sent along in transaction.payload
type StakingRequest struct {
	StakingOp   StakingOp
	StakingFrom uint64
	StakingTo   uint64
	StakingHash uint64
}

// Staker & StakingObject are the structures stored in
// StakingAddress's Account.Code for staking related information
// A single Staker
type Staker struct {
	// the Staker's address
	Address common.Address
	// the 1st block expected to participant in validator and proposer
	StakingFrom uint64
	// the last block to participant in validator and proposer, the tokens will be refunded
	// to participants' addresses after this block has mined
	StakingTo uint64
	// StakingHash is the last hash in hashchain,  random nubmers(R) in futureBlock
	// will be hashed for (futureBlock - stakingFrom) times to match with StakingHash.
	StakingHash uint64
	// records the number of tokens staked
	StakedValue *big.Int
}

// The object to be stored in StakingAddress's Account.Code
type StakingObject struct {
	Stakers []Staker // staker's, expired stakers will automatically be removed
}

// GetStakingObject returns the stakingObject at some state
func (e *BDLSEngine) GetStakingObject(state *state.StateDB) (*StakingObject, error) {
	var stakingObject StakingObject
	// retrieve committe data structure from code
	code := state.GetCode(StakingAddress)
	if code != nil {
		err := rlp.DecodeBytes(code, &stakingObject)
		if err != nil {
			return nil, err
		}
	}
	return &stakingObject, nil
}

// RandAtBlock calculates random number W based on block information
// W0 = H(U0)
// Wj = H(Pj-1,Wj-1) for 0<j<=r,
func (e *BDLSEngine) RandAtBlock(chain consensus.ChainReader, block *types.Block) common.Hash {
	if block.NumberU64() == 0 {
		return W0
	}

	// call RandAtABlock recursivly
	hasher := sha3.NewLegacyKeccak256()
	prevBlock := chain.GetBlock(block.ParentHash(), block.NumberU64()-1)
	coinbase := prevBlock.Coinbase()
	hasher.Write(coinbase[:])
	// TODO: if W has written in block header, then we can stop recursion.
	prevW := e.RandAtBlock(chain, prevBlock)
	hasher.Write(prevW[:])
	return common.BytesToHash(hasher.Sum(nil))
}

// H(r;0;Ri,r,0;Wr) > max{0;1 i-aip}
func (e *BDLSEngine) IsProposer(height uint64, W []byte, R common.Hash, numStaked *big.Int, totalStaked *big.Int) bool {
	// compute p
	p := big.NewFloat(0).SetInt(E1)
	p.Mul(p, big.NewFloat(0).SetInt(Alpha))
	p.Quo(p, big.NewFloat(0).SetInt(totalStaked))

	// max{0, 1 - ai*p}
	max := p.Sub(big.NewFloat(1), p.Mul(big.NewFloat(0).SetInt(numStaked), p))
	if max.Cmp(big.NewFloat(0)) != 1 {
		max = big.NewFloat(0)
	}

	// compute H
	hasher := sha3.New256()
	binary.Write(hasher, binary.LittleEndian, height)
	binary.Write(hasher, binary.LittleEndian, 0)
	hasher.Write(R[:])
	hasher.Write(W)

	// calculate H/MaxUint256
	h := big.NewFloat(0).SetInt(big.NewInt(0).SetBytes(hasher.Sum(nil)))
	h.Quo(h, MaxUint256)

	// prob compare
	if h.Cmp(max) == 1 {
		return true
	}
	return false
}

// deriveStakingSeed deterministically derives the random number for height, based on the staking from height and private key
// lastHash  = H(H(privatekey + stakingFrom) *G)
func (e *BDLSEngine) deriveStakingSeed(priv *ecdsa.PrivateKey, stakingFrom uint64) []byte {
	// H(privatekey + stakingFrom)
	hasher := sha3.New256()
	hasher.Write(priv.D.Bytes())
	binary.Write(hasher, binary.LittleEndian, stakingFrom)

	// H(privatekey + lastHeight) *G
	x, y := crypto.S256().ScalarBaseMult(hasher.Sum(nil))

	// H(H(privatekey + lastHeight) *G)
	hasher = sha3.New256()
	hasher.Write(x.Bytes())
	hasher.Write(y.Bytes())
	return hasher.Sum(nil)
}

// compute hash recursively for n(n>=0) times
func (e *BDLSEngine) hashChain(hash []byte, n int) []byte {
	if n == 0 {
		return hash
	}

	hasher := sha3.New256()
	hasher.Write(hash)
	for i := 1; i < n; i++ {
		hasher.Write(hasher.Sum(nil))
	}
	return hasher.Sum(nil)
}
