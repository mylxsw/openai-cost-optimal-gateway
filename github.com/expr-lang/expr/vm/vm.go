package vm

import (
	"fmt"
	"reflect"
)

type Program struct {
	Eval func(map[string]interface{}) (interface{}, error)
}

func Run(program *Program, env interface{}) (interface{}, error) {
	if program == nil || program.Eval == nil {
		return nil, fmt.Errorf("invalid program")
	}
	vars := make(map[string]interface{})
	if env != nil {
		extractEnv(vars, reflect.ValueOf(env))
	}
	return program.Eval(vars)
}

func extractEnv(dst map[string]interface{}, value reflect.Value) {
	if !value.IsValid() {
		return
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.Struct:
		t := value.Type()
		for i := 0; i < value.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" {
				continue
			}
			dst[field.Name] = value.Field(i).Interface()
		}
	case reflect.Map:
		iter := value.MapRange()
		for iter.Next() {
			key := iter.Key()
			if key.Kind() == reflect.String {
				dst[key.String()] = iter.Value().Interface()
			}
		}
	}
}
