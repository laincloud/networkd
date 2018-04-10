package server

import "github.com/deckarep/golang-set"

type suffixTreeNode struct {
	key      string
	value    mapset.Set
	children map[string]*suffixTreeNode
}

func newSuffixTreeRoot() *suffixTreeNode {
	return newSuffixTree("", "")
}

func newSuffixTree(key string, value string) *suffixTreeNode {
	v := mapset.NewSet()
	if value != "" {
		v.Add(value)
	}
	root := &suffixTreeNode{
		key:      key,
		value:    v,
		children: map[string]*suffixTreeNode{},
	}
	return root
}

func (node *suffixTreeNode) ensureSubTree(key string) {
	if _, ok := node.children[key]; !ok {
		node.children[key] = newSuffixTree(key, "")
	}
}

func (node *suffixTreeNode) insert(key string, value string) {
	if c, ok := node.children[key]; ok {
		c.value.Add(value)
	} else {
		node.children[key] = newSuffixTree(key, value)
	}
}

func (node *suffixTreeNode) sinsert(keys []string, value string) {
	if len(keys) == 0 {
		return
	}

	key := keys[len(keys)-1]
	if len(keys) > 1 {
		node.ensureSubTree(key)
		node.children[key].sinsert(keys[:len(keys)-1], value)
		return
	}

	node.insert(key, value)
}

func (node *suffixTreeNode) search(keys []string) (mapset.Set, bool) {
	if len(keys) == 0 {
		return nil, false
	}

	key := keys[len(keys)-1]
	if n, ok := node.children[key]; ok {
		if nextValue, found := n.search(keys[:len(keys)-1]); found {
			return nextValue, found
		}
		return n.value, n.value.Cardinality() != 0
	}
	return nil, false
}

func copyStringSlice(sl []string) []string {
	var new []string
	for _, v := range sl {
		new = append(new, v)
	}
	return new
}

func (node *suffixTreeNode) iterFunc(keys []string, fun func([]string, mapset.Set)) {
	if node.value.Cardinality() > 0 {
		fun(keys, node.value)
	}
	for k, child := range node.children {
		newKeys := copyStringSlice(keys)
		newKeys = append(newKeys, k)
		child.iterFunc(newKeys, fun)
	}
}
