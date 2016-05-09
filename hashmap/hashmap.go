package hashmap

import (
	"container/list"
	"sync"
)

type Item struct {
	key   string
	value interface{}
}

type HashMap struct {
	ll     *list.List
	RWLock sync.RWMutex
	items  map[string]*list.Element
}

func NewHashMap() *HashMap {
	return &HashMap{
		ll:    list.New(),
		items: make(map[string]*list.Element),
	}
}

func (db *HashMap) Add(key string, value interface{}) {
	if ee, ok := db.items[key]; ok {
		db.ll.MoveToFront(ee)
		ee.Value.(*Item).value = value
		return
	}
	ele := db.ll.PushFront(&Item{key: key, value: value})
	db.items[key] = ele
	return
}

func (db *HashMap) Get(key string) (value interface{}, ok bool) {
	if ee, ok := db.items[key]; ok {
		db.ll.MoveToFront(ee)
		return ee.Value.(*Item).value, true
	}
	return
}

func (db *HashMap) Remove(key string) {
	if ee, ok := db.items[key]; ok {
		db.ll.Remove(ee)
		delete(db.items, key)
	}
}

func (db *HashMap) Len() int {
	return db.ll.Len()
}

func (db *HashMap) Items() []interface{} {
	items := make([]interface{}, 0, db.ll.Len())
	for ee := db.ll.Front(); ee != nil; ee = ee.Next() {
		items = append(items, ee.Value.(*Item).value)
	}
	return items
}
