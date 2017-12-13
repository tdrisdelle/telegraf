package jsonlite

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/metric"
)

type JSONLiteParser struct {
	MetricName  string
	TagKeys     []string
	DefaultTags map[string]string
}

func (p *JSONLiteParser) parseArray(buf []byte) ([]telegraf.Metric, error) {
	metrics := make([]telegraf.Metric, 0)

	var jsonOut []map[string]interface{}
	err := json.Unmarshal(buf, &jsonOut)
	if err != nil {
		err = fmt.Errorf("unable to parse out as JSON Array, %s", err)
		return nil, err
	}
	for _, item := range jsonOut {
		metrics, err = p.parseObject(metrics, item)
	}
	return metrics, nil
}

func (p *JSONLiteParser) parseObject(metrics []telegraf.Metric, jsonOut map[string]interface{}) ([]telegraf.Metric, error) {

	tags := make(map[string]string)
	for k, v := range p.DefaultTags {
		tags[k] = v
	}

	for _, tag := range p.TagKeys {
		switch v := jsonOut[tag].(type) {
		case string:
			tags[tag] = v
		case bool:
			tags[tag] = strconv.FormatBool(v)
		case float64:
			tags[tag] = strconv.FormatFloat(v, 'f', -1, 64)
		}
		delete(jsonOut, tag)
	}

	f := JSONFlattener{TagKeys: p.TagKeys}
	err := f.FlattenJSON("", jsonOut)
	if err != nil {
		return nil, err
	}

	metric, err := metric.New(p.MetricName, tags, f.Fields, time.Now().UTC())

	if err != nil {
		return nil, err
	}
	return append(metrics, metric), nil
}

func (p *JSONLiteParser) Parse(buf []byte) ([]telegraf.Metric, error) {
	buf = bytes.TrimSpace(buf)
	if len(buf) == 0 {
		return make([]telegraf.Metric, 0), nil
	}

	if !isarray(buf) {
		metrics := make([]telegraf.Metric, 0)
		var jsonOut map[string]interface{}
		err := json.Unmarshal(buf, &jsonOut)
		if err != nil {
			err = fmt.Errorf("unable to parse out as JSON, %s", err)
			return nil, err
		}
		return p.parseObject(metrics, jsonOut)
	}
	return p.parseArray(buf)
}

func (p *JSONLiteParser) ParseLine(line string) (telegraf.Metric, error) {
	metrics, err := p.Parse([]byte(line + "\n"))

	if err != nil {
		return nil, err
	}

	if len(metrics) < 1 {
		return nil, fmt.Errorf("Can not parse the line: %s, for data format: influx ", line)
	}

	return metrics[0], nil
}

func (p *JSONLiteParser) SetDefaultTags(tags map[string]string) {
	p.DefaultTags = tags
}

type JSONFlattener struct {
	Fields  map[string]interface{}
	TagKeys []string
}

// FlattenJSON flattens nested maps/interfaces into a fields map (ignoring bools and string)
func (f *JSONFlattener) FlattenJSON(
	fieldname string,
	v interface{}) error {
	if f.Fields == nil {
		f.Fields = make(map[string]interface{})
	}
	return f.FullFlattenJSON(fieldname, v, false, false)
}

// FullFlattenJSON flattens nested maps/interfaces into a fields map (including bools and string)
func (f *JSONFlattener) FullFlattenJSON(
	fieldname string,
	v interface{},
	convertString bool,
	convertBool bool,
) error {
	if f.Fields == nil {
		f.Fields = make(map[string]interface{})
	}
	fieldname = strings.Trim(fieldname, "_")

	elts := strings.Split(fieldname, "_")
	tagFieldKey := elts[len(elts)-1]

	switch t := v.(type) {
	case map[string]interface{}:
		for k, v := range t {
			err := f.FullFlattenJSON(fieldname+"_"+k+"_", v, convertString, convertBool)
			if err != nil {
				return err
			}
		}
	case []interface{}:
		for i, v := range t {
			k := strconv.Itoa(i)
			err := f.FullFlattenJSON(fieldname+"_"+k+"_", v, convertString, convertBool)
			if err != nil {
				return nil
			}
		}
	case float64:
		if contains(f.TagKeys, tagFieldKey) {
			// do something
			f.Fields[fieldname] = t
		} else {
			return nil
		}
	case string:
		if convertString {
			f.Fields[fieldname] = v.(string)
		} else {
			if contains(f.TagKeys, tagFieldKey) {
				// do something
				f.Fields[fieldname] = v.(string)
			} else {
				return nil
			}
		}
	case bool:
		if convertBool {
			f.Fields[fieldname] = v.(bool)
		} else {
			if contains(f.TagKeys, tagFieldKey) {
				// do something
				f.Fields[fieldname] = v.(bool)
			} else {
				return nil
			}
		}
	case nil:
		return nil
	default:
		return fmt.Errorf("JSON Flattener: got unexpected type %T with value %v (%s)",
			t, t, fieldname)
	}
	return nil
}

func contains(a []string, s string) bool {
	for _, x := range a {
		if x == s {
			return true
		}
	}
	return false
}

func isarray(buf []byte) bool {
	ia := bytes.IndexByte(buf, '[')
	ib := bytes.IndexByte(buf, '{')
	if ia > -1 && ia < ib {
		return true
	} else {
		return false
	}
}
