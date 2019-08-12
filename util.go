package main

import "errors"

func convertToStringMap(x map[interface{}]interface{}) (map[string]interface{}, error) {
	if !isStringMap(x) {
		return nil, errors.New("Map has non-string keys")
	}

	out := make(map[string]interface{})

	for k, v := range x {
		stringKey := k.(string)

		newVal, err := stringMapConvertValue(v)
		if err != nil {
			return nil, err
		}
		out[stringKey] = newVal
	}

	return out, nil
}

func stringMapConvertValue(val interface{}) (interface{}, error) {
	mapVal, ok := val.(map[interface{}]interface{})
	if ok {
		return convertToStringMap(mapVal)
	}

	sliceVal, ok := val.([]interface{})
	if ok {
		return stringMapConvertSliceValue(sliceVal)
	}

	return val, nil
}

func stringMapConvertSliceValue(vals []interface{}) ([]interface{}, error) {
	out := make([]interface{}, 0)

	for _, v := range vals {
		newVal, err := stringMapConvertValue(v)
		if err != nil {
			return nil, err
		}
		out = append(out, newVal)
	}

	return out, nil
}

func isStringMap(m map[interface{}]interface{}) bool {
	for k := range m {
		_, ok := k.(string)
		if !ok {
			return false
		}
	}
	return true
}
