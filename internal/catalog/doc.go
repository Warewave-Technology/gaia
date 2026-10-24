// Package catalog loads Gaia objects (kind + name) from a Git commit,
// validates them against their kind schemas and resolves references between
// them into a graph. Nothing is applied when the graph has any error.
package catalog
