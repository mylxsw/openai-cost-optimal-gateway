package sjson

import "encoding/json"

func SetBytes(data []byte, path string, value interface{}) ([]byte, error) {
	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	root[path] = value
	return json.Marshal(root)
}
