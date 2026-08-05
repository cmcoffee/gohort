package notes

import "reflect"

// memStore is a Store for tests. The leaf can't reach core's DBase, and it
// doesn't need to: nothing here exercises serialization, only round-tripping.
type memStore struct{ m map[string]any }

func newMemStore() *memStore { return &memStore{m: map[string]any{}} }

func (s *memStore) Get(table, key string, out interface{}) bool {
	v, ok := s.m[table+"\x00"+key]
	if !ok {
		return false
	}
	dst := reflect.ValueOf(out)
	src := reflect.ValueOf(v)
	if dst.Kind() != reflect.Ptr || !src.Type().AssignableTo(dst.Elem().Type()) {
		return false
	}
	dst.Elem().Set(src)
	return true
}

func (s *memStore) Set(table, key string, value interface{}) { s.m[table+"\x00"+key] = value }
func (s *memStore) Unset(table, key string)                  { delete(s.m, table+"\x00"+key) }
func (s *memStore) Keys(table string) []string {
	var out []string
	for k := range s.m {
		if i := len(table); len(k) > i && k[:i] == table && k[i] == 0 {
			out = append(out, k[i+1:])
		}
	}
	return out
}
