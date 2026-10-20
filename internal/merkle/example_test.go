package merkle

import (
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
)

// exampleFill32 returns a [32]byte with every byte set to b, so the
// example's fixture values are visible and distinct at a glance.
func exampleFill32(b byte) [32]byte {
	var out [32]byte
	for i := range out {
		out[i] = b
	}
	return out
}

// ExampleBuildTree shows how BuildTree constructs a sorted-pair Merkle tree
// over concrete 32-byte leaf hashes, producing a root hash and per-leaf proofs
// that match the statement_registry contract's fold algorithm.
func ExampleBuildTree() {
	// Construct concrete literal leaf inputs.
	leaves := [][32]byte{
		exampleFill32(0x11),
		exampleFill32(0x22),
		exampleFill32(0x33),
	}

	tree, err := BuildTree(leaves)
	if err != nil {
		log.Fatalf("BuildTree: %v", err)
	}

	fmt.Printf("Root: %x\n", tree.Root)
	fmt.Printf("Proof count: %d\n", len(tree.Proofs))
	fmt.Printf("First leaf proof length: %d\n", len(tree.Proofs[0]))
	fmt.Println("First leaf verifies against root:", VerifyProof(tree.Root, leaves[0], tree.Proofs[0]))

	// Output:
	// Root: 277b6f43115f5bfd44a875c69575ec332ca5cae7eb76566270a122038611e48f
	// Proof count: 3
	// First leaf proof length: 2
	// First leaf verifies against root: true
}

// ExampleProof shows how a caller obtains and uses a Proof.
//
// The type's doc comment turns on one design decision: a Proof is an
// ordered list of sibling hashes with no leaf index, so it proves
// membership of its leaf value, never a position, and reproduces the root
// only when the fold order is preserved.
//
// The caller-facing consequence is that a Proof's elements are never
// set field-by-field from a zero value. There are exactly two ways a
// caller has one: BuildTree returns it already populated and the caller
// leaves the elements alone, or a proof produced elsewhere arrives as
// sibling nodes in fold order and the caller decodes them in that order —
// the one construction a caller performs. The zero value is correct for
// one case only: a single-leaf tree, whose root is the leaf itself.
func ExampleProof() {
	records := []Record{
		{
			Amount:       big.NewInt(100),
			Consumer:     testConsumer,
			EndpointHash: EndpointHash("GET", "/v1/inference"),
			Ledger:       100,
			PriceVersion: 1,
			RequestID:    exampleFill32(0xAA),
			Units:        1,
		},
		{
			Amount:       big.NewInt(250),
			Consumer:     testConsumer,
			EndpointHash: EndpointHash("GET", "/v1/inference"),
			Ledger:       101,
			PriceVersion: 1,
			RequestID:    exampleFill32(0xBB),
			Units:        3,
		},
		{
			Amount:       big.NewInt(175),
			Consumer:     testConsumer,
			EndpointHash: EndpointHash("POST", "/v1/embed"),
			Ledger:       102,
			PriceVersion: 1,
			RequestID:    exampleFill32(0xCC),
			Units:        2,
		},
	}
	leaves := make([][32]byte, len(records))
	for i, r := range records {
		leaf, err := Leaf(r)
		if err != nil {
			log.Fatalf("Leaf: %v", err)
		}
		leaves[i] = leaf
	}

	tree, err := BuildTree(leaves)
	if err != nil {
		log.Fatalf("BuildTree: %v", err)
	}

	// The proof for the second record's leaf. BuildTree has already filled
	// in every element — the sibling hashes and their order — so the caller
	// takes it as-is and folds it against a root it trusts. Nothing here
	// writes into the slice.
	proof := tree.Proofs[1]
	fmt.Println("BuildTree's proof verifies against its root:", VerifyProof(tree.Root, leaves[1], proof))

	// The same proof on the wire: a proof produced elsewhere — an anchoring
	// bundle, a proof service — carries one hex string per sibling node, in
	// fold order.
	wire := make([]string, len(proof))
	for i, node := range proof {
		wire[i] = hex.EncodeToString(node[:])
	}

	// The caller-side reconstruction, the one way a caller builds a Proof
	// itself: decode each sibling in the order received, appending to the
	// proof. Reordering the nodes changes what the fold computes, and a
	// truncated proof folds to some other value — neither is a position,
	// because the format has no leaf index.
	var received Proof
	for _, chunk := range wire {
		raw, err := hex.DecodeString(chunk)
		if err != nil {
			log.Fatalf("decode proof node: %v", err)
		}
		var node [32]byte
		copy(node[:], raw)
		received = append(received, node)
	}
	fmt.Printf("proof has %d sibling(s); decoded proof verifies: %v\n", len(received), VerifyProof(tree.Root, leaves[1], received))

	// The zero value is not a shortcut for "a valid proof": with no
	// siblings, Fold can only return the leaf itself. It verifies against a
	// root only when that root IS the leaf — a single-leaf tree.
	fmt.Println("zero-value Proof against a multi-leaf tree's root:", VerifyProof(tree.Root, leaves[1], Proof{}))

	single, err := BuildTree(leaves[2:3])
	if err != nil {
		log.Fatalf("BuildTree single leaf: %v", err)
	}
	fmt.Println("zero-value Proof verifies a single-leaf tree whose root is the leaf:", VerifyProof(single.Root, leaves[2], Proof{}))

	// Output:
	//
	// BuildTree's proof verifies against its root: true
	// proof has 2 sibling(s); decoded proof verifies: true
	// zero-value Proof against a multi-leaf tree's root: false
	// zero-value Proof verifies a single-leaf tree whose root is the leaf: true
}
