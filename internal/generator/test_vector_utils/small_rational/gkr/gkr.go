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

package gkr

import (
	"github.com/consensys/gnark-crypto/internal/generator/test_vector_utils/small_rational"
	"github.com/consensys/gnark-crypto/internal/generator/test_vector_utils/small_rational/polynomial"
	"github.com/consensys/gnark-crypto/internal/generator/test_vector_utils/small_rational/sumcheck"
	"sync"
)

// The goal is to prove/verify evaluations of many instances of the same circuit

// Gate must be a low-degree polynomial
type Gate interface {
	Evaluate(...small_rational.SmallRational) small_rational.SmallRational
	Degree() int
}

type Wire struct {
	Gate      Gate
	Inputs    []*Wire // if there are no Inputs, the wire is assumed an input wire
	nbOutputs int     // number of other wires using it as input, not counting doubles (i.e. providing two inputs to the same gate counts as one)
	//metadata  string // names and the like, for debugging
}

type Circuit []Wire

func (w Wire) IsInput() bool {
	return len(w.Inputs) == 0
}

func (w Wire) IsOutput() bool {
	return w.nbOutputs == 0
}

func (w Wire) NbClaims() int {
	if w.IsOutput() {
		return 1
	}
	return w.nbOutputs
}

// WireAssignment is assignment of values to the same wire across many instances of the circuit
type WireAssignment map[*Wire]polynomial.MultiLin

type Proof []sumcheck.Proof // for each layer, for each wire, a sumcheck (for each variable, a polynomial)

type eqTimesGateEvalSumcheckLazyClaims struct {
	wire               *Wire
	evaluationPoints   [][]small_rational.SmallRational
	claimedEvaluations []small_rational.SmallRational
	manager            *claimsManager // WARNING: Circular references
}

func (e *eqTimesGateEvalSumcheckLazyClaims) ClaimsNum() int {
	return len(e.evaluationPoints)
}

func (e *eqTimesGateEvalSumcheckLazyClaims) VarsNum() int {
	return len(e.evaluationPoints[0])
}

func (e *eqTimesGateEvalSumcheckLazyClaims) CombinedSum(a small_rational.SmallRational) small_rational.SmallRational {
	evalsAsPoly := polynomial.Polynomial(e.claimedEvaluations)
	return evalsAsPoly.Eval(&a)
}

func (e *eqTimesGateEvalSumcheckLazyClaims) Degree(int) int {
	return 1 + e.wire.Gate.Degree()
}

func (e *eqTimesGateEvalSumcheckLazyClaims) VerifyFinalEval(r []small_rational.SmallRational, combinationCoeff small_rational.SmallRational, purportedValue small_rational.SmallRational, proof interface{}) bool {
	inputEvaluations := proof.([]small_rational.SmallRational)

	numClaims := len(e.evaluationPoints)

	evaluation := polynomial.EvalEq(e.evaluationPoints[numClaims-1], r)
	for i := numClaims - 2; i >= 0; i-- {
		evaluation.Mul(&evaluation, &combinationCoeff)
		eq := polynomial.EvalEq(e.evaluationPoints[i], r)
		evaluation.Add(&evaluation, &eq)
	}

	var gateEvaluation small_rational.SmallRational
	if e.wire.IsInput() {
		gateEvaluation = e.manager.assignment[e.wire].Evaluate(r, e.manager.memPool)
	} else {
		gateEvaluation = e.wire.Gate.Evaluate(inputEvaluations...)
		// defer verification, store the new claims
		e.manager.addForInput(e.wire, r, inputEvaluations)
	}

	evaluation.Mul(&evaluation, &gateEvaluation)

	return evaluation.Equal(&purportedValue)
}

type eqTimesGateEvalSumcheckClaims struct {
	wire               *Wire
	evaluationPoints   [][]small_rational.SmallRational // x in the paper
	claimedEvaluations []small_rational.SmallRational   // y in the paper
	manager            *claimsManager

	inputPreprocessors []polynomial.MultiLin // P_u in the paper, so that we don't need to pass along all the circuit's evaluations

	eq polynomial.MultiLin // ∑_i τ_i eq(x_i, -)
}

func (c *eqTimesGateEvalSumcheckClaims) Combine(combinationCoeff small_rational.SmallRational) polynomial.Polynomial {
	varsNum := c.VarsNum()
	eqLength := 1 << varsNum
	claimsNum := c.ClaimsNum()
	// initialize the eq tables
	c.eq = c.manager.memPool.Make(eqLength)

	c.eq[0].SetOne()
	c.eq.Eq(c.evaluationPoints[0])

	newEq := polynomial.MultiLin(c.manager.memPool.Make(eqLength))
	aI := combinationCoeff

	for k := 1; k < claimsNum; k++ { //TODO: parallelizable?
		// define eq_k = aᵏ eq(x_k1, ..., x_kn, *, ..., *) where x_ki are the evaluation points
		newEq[0].Set(&aI)
		newEq.Eq(c.evaluationPoints[k])

		eqAsPoly := polynomial.Polynomial(c.eq) //just semantics
		eqAsPoly.Add(eqAsPoly, polynomial.Polynomial(newEq))

		if k+1 < claimsNum {
			aI.Mul(&aI, &combinationCoeff)
		}
	}

	c.manager.memPool.Dump(newEq)

	// from this point on the claim is a rather simple one: g = E(h) × R_v (P_u0(h), ...) where E and the P_u are multilinear and R_v is of low-degree

	return c.computeGJ()
}

// computeValAndStep returns val : i ↦ m(1, i...) and step : i ↦ m(1, i...) - m(0, i...)
func computeValAndStep(m polynomial.MultiLin, p *polynomial.Pool) (val polynomial.MultiLin, step polynomial.MultiLin) {
	val = p.Clone(m[len(m)/2:])
	step = p.Clone(m[:len(m)/2])

	valAsPoly, stepAsPoly := polynomial.Polynomial(val), polynomial.Polynomial(step)

	stepAsPoly.Sub(valAsPoly, stepAsPoly)
	return
}

// computeGJ: gⱼ = ∑_{0≤i<2ⁿ⁻ʲ} g(r₁, r₂, ..., rⱼ₋₁, Xⱼ, i...) = ∑_{0≤i<2ⁿ⁻ʲ} E(r₁, ..., X_j, i...) R_v( P_u0(r₁, ..., X_j, i...), ... ) where  E = ∑ eq_k
// the polynomial is represented by the evaluations g_j(1), g_j(2), ..., g_j(deg(g_j)).
// The value g_j(0) is inferred from the equation g_j(0) + g_j(1) = g_{j-1}(r_{j-1}). By convention, g_0 is a constant polynomial equal to the claimed sum.
func (c *eqTimesGateEvalSumcheckClaims) computeGJ() (gJ polynomial.Polynomial) {

	// Let f ∈ { E(r₁, ..., X_j, d...) } ∪ {P_ul(r₁, ..., X_j, d...) }. It is linear in X_j, so f(m) = m×(f(1) - f(0)) + f(0), and f(0), f(1) are easily computed from the bookkeeping tables
	EVal, EStep := computeValAndStep(c.eq, c.manager.memPool)

	puVal := make([]polynomial.MultiLin, len(c.inputPreprocessors))  //TODO: Make a two-dimensional array struct, and index it i-first rather than inputI first: would result in scanning memory access in the "d" loop and obviate the gateInput variable
	puStep := make([]polynomial.MultiLin, len(c.inputPreprocessors)) //TODO, ctd: the greater degGJ, the more this would matter

	for i, puI := range c.inputPreprocessors {
		puVal[i], puStep[i] = computeValAndStep(puI, c.manager.memPool)
	}

	degGJ := 1 + c.wire.Gate.Degree() // guaranteed to be no smaller than the actual deg(g_j)
	gJ = make([]small_rational.SmallRational, degGJ)

	parallel := len(EVal) >= 1024 //TODO: Experiment with threshold

	var gateInput [][]small_rational.SmallRational

	if parallel {
		gateInput = [][]small_rational.SmallRational{c.manager.memPool.Make(len(c.inputPreprocessors)),
			c.manager.memPool.Make(len(c.inputPreprocessors))}
	} else {
		gateInput = [][]small_rational.SmallRational{c.manager.memPool.Make(len(c.inputPreprocessors))}
	}

	var wg sync.WaitGroup

	for d := 0; d < degGJ; d++ {

		notLastIteration := d+1 < degGJ

		sumOverI := func(res *small_rational.SmallRational, gateInput []small_rational.SmallRational, start, end int) {
			for i := start; i < end; i++ {

				for inputI := range puVal {
					gateInput[inputI].Set(&puVal[inputI][i])
					if notLastIteration {
						puVal[inputI][i].Add(&puVal[inputI][i], &puStep[inputI][i])
					}
				}

				// gJAtDI = gJ(d, i...)
				gJAtDI := c.wire.Gate.Evaluate(gateInput...)
				gJAtDI.Mul(&gJAtDI, &EVal[i])

				res.Add(res, &gJAtDI)

				if notLastIteration {
					EVal[i].Add(&EVal[i], &EStep[i])
				}
			}
			wg.Done()
		}

		if parallel {
			var firstHalf, secondHalf small_rational.SmallRational
			wg.Add(2)
			go sumOverI(&secondHalf, gateInput[1], len(EVal)/2, len(EVal))
			go sumOverI(&firstHalf, gateInput[0], 0, len(EVal)/2)
			wg.Wait()
			gJ[d].Add(&firstHalf, &secondHalf)
		} else {
			wg.Add(1) // formalities
			sumOverI(&gJ[d], gateInput[0], 0, len(EVal))
		}
	}

	c.manager.memPool.Dump(gateInput...)
	c.manager.memPool.Dump(EVal, EStep)

	for inputI := range puVal {
		c.manager.memPool.Dump(puVal[inputI], puStep[inputI])
	}

	return
}

// Next first folds the "preprocessing" and "eq" polynomials then compute the new g_j
func (c *eqTimesGateEvalSumcheckClaims) Next(element small_rational.SmallRational) polynomial.Polynomial {
	c.eq.Fold(element)
	for i := 0; i < len(c.inputPreprocessors); i++ {
		c.inputPreprocessors[i].Fold(element)
	}
	return c.computeGJ()
}

func (c *eqTimesGateEvalSumcheckClaims) VarsNum() int {
	return len(c.evaluationPoints[0])
}

func (c *eqTimesGateEvalSumcheckClaims) ClaimsNum() int {
	return len(c.claimedEvaluations)
}

func (c *eqTimesGateEvalSumcheckClaims) ProveFinalEval(r []small_rational.SmallRational) interface{} {

	//defer the proof, return list of claims
	evaluations := make([]small_rational.SmallRational, len(c.inputPreprocessors))
	for i, puI := range c.inputPreprocessors {
		puI.Fold(r[len(r)-1])

		if len(puI) != 1 {
			panic("must be one") //TODO: Remove
		}

		evaluations[i].Set(&puI[0])
		c.manager.memPool.Dump(puI)
	}

	c.manager.memPool.Dump(c.claimedEvaluations, c.eq)

	c.manager.addForInput(c.wire, r, evaluations)

	return evaluations
}

type claimsManager struct {
	claimsMap  map[*Wire]*eqTimesGateEvalSumcheckLazyClaims
	assignment WireAssignment
	memPool    *polynomial.Pool
}

func newClaimsManager(c Circuit, assignment WireAssignment, pool *polynomial.Pool) (claims claimsManager) {
	claims.assignment = assignment
	claims.claimsMap = make(map[*Wire]*eqTimesGateEvalSumcheckLazyClaims, len(c))

	if pool == nil {

		// extract the number of instances. TODO: Clean way?
		nInstances := 0
		for _, a := range assignment {
			nInstances = len(a)
			break
		}

		pool := polynomial.NewPool(1<<11, nInstances)
		claims.memPool = &pool
	} else {
		claims.memPool = pool
	}

	for i := range c {
		wire := &c[i]

		claims.claimsMap[wire] = &eqTimesGateEvalSumcheckLazyClaims{
			wire:               wire,
			evaluationPoints:   make([][]small_rational.SmallRational, 0, wire.NbClaims()),
			claimedEvaluations: claims.memPool.Make(wire.NbClaims()),
			manager:            &claims,
		}
	}
	return
}

func (m *claimsManager) add(wire *Wire, evaluationPoint []small_rational.SmallRational, evaluation small_rational.SmallRational) {
	claim := m.claimsMap[wire]
	i := len(claim.evaluationPoints)
	claim.claimedEvaluations[i] = evaluation
	claim.evaluationPoints = append(claim.evaluationPoints, evaluationPoint)
}

// addForInput claims regarding all inputs to the wire, all evaluated at the same point
func (m *claimsManager) addForInput(wire *Wire, evaluationPoint []small_rational.SmallRational, evaluations []small_rational.SmallRational) {
	wiresWithClaims := make(map[*Wire]struct{}) // In case the gate takes the same wire as input multiple times, one claim would suffice

	for inputI, inputWire := range wire.Inputs {
		if _, found := wiresWithClaims[inputWire]; !found { //skip repeated claims
			wiresWithClaims[inputWire] = struct{}{}
			m.add(inputWire, evaluationPoint, evaluations[inputI])
		}
	}
}

func (m *claimsManager) getLazyClaim(wire *Wire) *eqTimesGateEvalSumcheckLazyClaims {
	return m.claimsMap[wire]
}

func (m *claimsManager) getClaim(wire *Wire) *eqTimesGateEvalSumcheckClaims {
	lazy := m.claimsMap[wire]
	res := &eqTimesGateEvalSumcheckClaims{
		wire:               wire,
		evaluationPoints:   lazy.evaluationPoints,
		claimedEvaluations: lazy.claimedEvaluations,
		manager:            m,
	}

	if wire.IsInput() {
		wire.Gate = IdentityGate{} // a bit dirty, modifying data structure given from outside
		res.inputPreprocessors = []polynomial.MultiLin{m.memPool.Clone(m.assignment[wire])}
	} else {
		res.inputPreprocessors = make([]polynomial.MultiLin, len(wire.Inputs))

		for inputI, inputW := range wire.Inputs {
			res.inputPreprocessors[inputI] = m.memPool.Clone(m.assignment[inputW]) //will be edited later, so must be deep copied
		}
	}
	return res
}

func (m *claimsManager) deleteClaim(wire *Wire) {
	delete(m.claimsMap, wire)
}

type _options struct {
	pool *polynomial.Pool
}

type Option func(*_options)

func WithPool(pool *polynomial.Pool) Option {
	return func(options *_options) {
		options.pool = pool
	}
}

// Prove consistency of the claimed assignment
func Prove(c Circuit, assignment WireAssignment, transcript sumcheck.ArithmeticTranscript, options ...Option) Proof {
	var o _options
	for _, option := range options {
		option(&o)
	}
	cS := TopologicalSort(c)

	claims := newClaimsManager(c, assignment, o.pool)

	proof := make(Proof, len(c))
	// firstChallenge called rho in the paper
	firstChallenge := transcript.NextN(assignment[&c[0]].NumVars()) //TODO: Clean way to extract numVars

	for i := len(c) - 1; i >= 0; i-- {

		wire := cS[i]

		if wire.IsOutput() {
			claims.add(wire, firstChallenge, assignment[wire].Evaluate(firstChallenge, claims.memPool))
		}

		claim := claims.getClaim(wire)
		if wire.IsInput() && claim.ClaimsNum() == 1 || claim.ClaimsNum() == 0 { // no proof necessary
			proof[i] = sumcheck.Proof{
				PartialSumPolys: []polynomial.Polynomial{},
				FinalEvalProof:  []small_rational.SmallRational{},
			}
		} else {
			proof[i] = sumcheck.Prove(claim, transcript)
			if finalEvalProof := proof[i].FinalEvalProof.([]small_rational.SmallRational); len(finalEvalProof) != 0 {
				transcript.Update(sumcheck.ElementSliceToInterfaceSlice(finalEvalProof)...)
			}
		}
		// the verifier checks a single claim about input wires itself
		claims.deleteClaim(wire)
	}

	return proof
}

// Verify the consistency of the claimed output with the claimed input
// Unlike in Prove, the assignment argument need not be complete
func Verify(c Circuit, assignment WireAssignment, proof Proof, transcript sumcheck.ArithmeticTranscript, options ...Option) bool {
	var o _options
	for _, option := range options {
		option(&o)
	}
	cS := TopologicalSort(c)

	claims := newClaimsManager(c, assignment, o.pool)

	firstChallenge := transcript.NextN(assignment[&c[0]].NumVars()) //TODO: Clean way to extract numVars

	for i := len(c) - 1; i >= 0; i-- {
		wire := cS[i]

		if wire.IsOutput() {
			claims.add(wire, firstChallenge, assignment[wire].Evaluate(firstChallenge, claims.memPool))
		}

		proofW := proof[i]
		finalEvalProof := proofW.FinalEvalProof.([]small_rational.SmallRational)
		claim := claims.getLazyClaim(wire)
		if claimsNum := claim.ClaimsNum(); wire.IsInput() && claimsNum == 1 || claimsNum == 0 {
			// make sure the proof is empty
			if len(finalEvalProof) != 0 || len(proofW.PartialSumPolys) != 0 {
				return false
			}

			if claimsNum == 1 {
				// simply evaluate and see if it matches
				evaluation := assignment[wire].Evaluate(claim.evaluationPoints[0], claims.memPool)
				if !claim.claimedEvaluations[0].Equal(&evaluation) {
					return false
				}
			}
		} else if !sumcheck.Verify(claim, proof[i], transcript) {
			return false //TODO: Any polynomials to dump?
		}
		if len(finalEvalProof) != 0 {
			transcript.Update(sumcheck.ElementSliceToInterfaceSlice(finalEvalProof)...)
		}
		claims.deleteClaim(wire)
	}
	return true
}

type IdentityGate struct{}

func (IdentityGate) Evaluate(input ...small_rational.SmallRational) small_rational.SmallRational {
	return input[0]
}

func (IdentityGate) Degree() int {
	return 1
}

// outputsList also sets the nbOutputs fields
func outputsList(c Circuit, indexes map[*Wire]int) [][]int {
	res := make([][]int, len(c))
	for i := range c {
		res[i] = make([]int, 0)
		c[i].nbOutputs = 0
	}
	ins := make(map[int]struct{}, len(c))
	for i := range c {
		for k := range ins { // clear map
			delete(ins, k)
		}
		for _, in := range c[i].Inputs {
			inI := indexes[in]
			res[inI] = append(res[inI], i)
			if _, ok := ins[inI]; !ok {
				in.nbOutputs++
				ins[inI] = struct{}{}
			}
		}
	}
	return res
}

type topSortData struct {
	outputs    [][]int
	status     []int // status > 0 indicates number of inputs left to be ready. status = 0 means ready. status = -1 means done
	index      map[*Wire]int
	leastReady int
}

func (d *topSortData) markDone(i int) {

	d.status[i] = -1

	for _, outI := range d.outputs[i] {
		d.status[outI]--
		if d.status[outI] == 0 && outI < d.leastReady {
			d.leastReady = outI
		}
	}

	for d.leastReady < len(d.status) && d.status[d.leastReady] != 0 {
		d.leastReady++
	}
}

func indexMap(c Circuit) map[*Wire]int {
	res := make(map[*Wire]int, len(c))
	for i := range c {
		res[&c[i]] = i
	}
	return res
}

func statusList(c Circuit) []int {
	res := make([]int, len(c))
	for i := range c {
		res[i] = len(c[i].Inputs)
	}
	return res
}

// TopologicalSort sorts the wires in order of dependence. Such that for any wire, any one it depends on
// occurs before it. It tries to stick to the input order as much as possible. An already sorted list will remain unchanged.
// It also sets the nbOutput flags. Worst-case inefficient O(n^2), but that probably won't matter since the circuits are small.
// Furthermore, it is efficient with already-close-to-sorted lists, which are the expected input
func TopologicalSort(c Circuit) []*Wire {
	var data topSortData
	data.index = indexMap(c)
	data.outputs = outputsList(c, data.index)
	data.status = statusList(c)
	sorted := make([]*Wire, len(c))

	for data.leastReady = 0; data.status[data.leastReady] != 0; data.leastReady++ {
	}

	for i := range c {
		sorted[i] = &c[data.leastReady]
		data.markDone(data.leastReady)
	}

	return sorted
}

// Complete the circuit evaluation from input values
func (a WireAssignment) Complete(c Circuit) WireAssignment {

	sortedWires := TopologicalSort(c)

	numEvaluations := 0

	for _, w := range sortedWires {
		if !w.IsInput() {
			if numEvaluations == 0 {
				numEvaluations = len(a[w.Inputs[0]])
			}
			evals := make([]small_rational.SmallRational, numEvaluations)
			ins := make([]small_rational.SmallRational, len(w.Inputs))
			for k := 0; k < numEvaluations; k++ {
				for inI, in := range w.Inputs {
					ins[inI] = a[in][k]
				}
				evals[k] = w.Gate.Evaluate(ins...)
			}
			a[w] = evals
		}
	}
	return a
}
