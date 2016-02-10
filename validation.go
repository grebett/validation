// This package handles data validation
package validation

import (
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"regexp"
	"strings"

	"github.com/grebett/tools"
	"gopkg.in/mgo.v2/bson"
)

//***********************************************************************************
//                                   CONSTANTS
//***********************************************************************************

const (
	UNAUTHENTICATED = iota
	USER
	OWNER
	ADMIN
	NONE
)

const (
	INIT = iota
	GET
	SET
)

//***********************************************************************************
//                                 STRUCTURES
//***********************************************************************************

// DataErrors are detailed errors when receiving or manipulating data
type DataError struct {
	Type   string      `json:"type"`
	Reason string      `json:"reason"`
	Field  string      `json:"field,omitempty"`
	Value  interface{} `json:"value,omitempty"`
}

// This struct hosts the Validate fn secondary parameters
type Options struct {
	Usage      int         // INIT, SET, GET
	UserRights int         // UNAUTHENTICATED to ADMIN
	Args       interface{} // custom args to be used with Default fn
}

// Error stringer for DataErrors
func (e *DataError) Error() string {
	return fmt.Sprintf("%s for %s = %v: %s", e.Type, e.Field, e.Value, e.Reason)
}

// This struct contains information about a specifical fields – could be a separated package later
type Validator struct {
	Type       string                               // the string representation of the expected type
	Field      string                               // the key the validator is about
	Regexp     string                               // if a string, the pattern the valus has to match
	Rights     [3]int                               // INIT, GET, SET minimal value to equal to act on the field value
	Boundaries Boundaries                           // if a number, the min and max boundaries for the value
	IsRequired bool                                 // is the field required
	Default    func(interface{}) interface{}        // this function is called to replace the optional nil value with default one - the arg interface{} value is usually a map[string]interface{} -- should I change it?
	CustomTest func(interface{}) (bool, *DataError) // this function enables user custom testing
}

// This inner struct sets the boundaries for an int value - see above
type Boundaries struct {
	Min float64
	Max float64
}

//***********************************************************************************
//                                   METHODS - why no private?
//***********************************************************************************

// This method create a regexp from the pattern defined in the Validation struct and test it for the provided string
func (v *Validator) ExecRegexp(str string) (bool, error) {
	validate, err := regexp.Compile(v.Regexp)
	if err != nil {
		return false, err
	}
	return validate.MatchString(str), nil
}

// This method test if the provided int fits in the validator boundaries
func (v *Validator) CheckBoundaries(value float64) bool {
	return value >= v.Boundaries.Min && value <= v.Boundaries.Max
}

// This method checks if the user has the rights for the specified usage
func (v *Validator) CheckRights(userRights int, usage int) bool {
	return userRights >= usage
}

//***********************************************************************************
//                                  FUNCTIONS
//***********************************************************************************

// Requirements
// ------------

//	*errors = append(*errors, &DataError{Type: "Validation error", Reason: "Required", Field: field})

// Data validation
// ---------------

// This public function runs the provided validators against the provided data
// The usage int is an enum for INIT, GET or SET value
// the checkValue flag enables a more complex validation -- is it still needed?
func Validate(validators map[string]*Validator, _map map[string]interface{}, opt Options) (map[string]interface{}, []*DataError) {
	errors := make([]*DataError, 0)
	dest := make(map[string]interface{})

	// browse the validators and get the path they are written for
	for path, validator := range validators {
		// get the value
		value, err := tools.ReadDeep(_map, path)
		if err != nil {
			panic(err)
		} else {
			// if the value is nil or is a slice with len == 0
			if value == nil || (reflect.ValueOf(value).Kind() == reflect.Slice && len(value.([]interface{})) == 0) {
				// for INIT only, if value does not exist, check in the validators if it is required and apply defaults accordingly
				if opt.Usage == INIT {
					// does not check for now if the slice is not nil but has nil values in it...
					if validator.IsRequired {
						errors = append(errors, &DataError{Type: "Validation error", Reason: "Required", Field: path})
					} else if validator.Default != nil {
						err := tools.WriteDeep(dest, path, validator.Default(opt.Args))
						if err != nil {
							panic(err)
						}
					}
				}
				// else the field is simply ignored
				continue
			} else {
				// copy value to dest (differs based on usage: mongoDB need dot notation for update --> https://docs.mongodb.org/manual/reference/glossary/#term-dot-notation)
				if opt.Usage == SET {
					dest[path] = value
				} else {
					err := tools.WriteDeep(dest, path, value)
					if err != nil {
						panic(err)
					}
				}

				// check type
				if checkType(validator, value, &errors) == false {
					continue
				}

				// check requirements
				if checkValue(validator, value, &errors) == false {
					continue
				}
				// check rights
				if checkRights(validator, opt.Usage, opt.UserRights, &errors) == false {
					continue // not useful, but for consistancy
				}
			}
		}
	}
	return dest, errors
}

// this private function runs the rights validator
// -----------------------------------------------
// the rights property of the validator is of type [3]int
// the three indexes of the array correspond to :
// 0 => initialization rights: the rights needed to set the property when creating the document
// 1 => get rights: can the user get this property?
// 2 => set rights: can the user update the property value?
// rights values are, in order: UNAUTHENTICATED, USER, OWNER, ADMIN, NONE
// returns true if everything is ok, false otherelse (could be the contrary)
func checkRights(validator *Validator, usage int, userRights int, errors *[]*DataError) bool {
	if ok := validator.CheckRights(userRights, validator.Rights[usage]); !ok {
		*errors = append(*errors, &DataError{Type: "Validation error", Reason: "Insufficient rights", Field: validator.Field})
		return false
	}
	return true
}

// this private function runs the validator according to the provided field if existing
// the validator checks different conditions, some based on value type
// returns true if everything is ok, false otherelse (could be the contrary)
func checkValue(validator *Validator, valueToTest interface{}, errors *[]*DataError) bool {
	// test based on value's type
	switch value := valueToTest.(type) {
	case string:
		if validator.Regexp != "" {
			ok, err := validator.ExecRegexp(value)
			if err != nil {
				log.Panic(err) // if the regexp is false, panic!
			} else {
				if !ok {
					*errors = append(*errors, &DataError{"Validation error", "Regex not match", validator.Field, value})
					return false
				}
			}
		}
	case json.Number:
		n, _ := value.Float64()
		if ok := validator.CheckBoundaries(n); !ok {
			*errors = append(*errors, &DataError{"Validation error", "Out of boundaries", validator.Field, value})
			return false
		}
	}

	// user's custom test
	if validator.CustomTest != nil {
		ok, err := validator.CustomTest(valueToTest)
		if !ok {
			*errors = append(*errors, err)
			return false
		}
	}

	return true
}

// This function check if the real type behind the interface value is the one wished by the validators
func checkType(validator *Validator, valueToTest interface{}, errors *[]*DataError) bool {
	kind := reflect.ValueOf(valueToTest).Kind()
	switch kind {
	case reflect.Slice:
		array := validator.Type[0:2] // indeed, the type representation string begins with []
		_type := validator.Type[2:]  // here we have the type after []
		if array != "[]" {
			*errors = append(*errors, &DataError{Type: "Validation error", Reason: "Type mismatch", Field: validator.Field, Value: reflect.TypeOf(valueToTest).String()})
			return false
		} else {
			for _, value := range valueToTest.([]interface{}) {
				vtype := reflect.TypeOf(value).String()
				if vtype != _type {
					// _type can be bson.ObjectId... which is basicly a string. So the condition above may fail but the type is in fact correct. Let's check:
					if stringValue, ok := value.(string); ok && _type == "bson.ObjectId" && bson.IsObjectIdHex(stringValue) {
						return true
					} else {
						*errors = append(*errors, &DataError{Type: "Validation error", Reason: "Type mismatch", Field: validator.Field, Value: "[] contains " + reflect.TypeOf(value).String()})
						return false
					}
				}
			}
		}
	case reflect.Map:
		// json maps are map[string]interface{}, but we could test for more...
		parts := strings.SplitAfter(validator.Type, "]")
		for key, value := range valueToTest.(map[string]interface{}) {
			vtype := reflect.TypeOf(value)

			// such as is the string key a correct ObjectId ?
			if parts[0] == "map[bson.ObjectId]" {
				if !bson.IsObjectIdHex(key) {
					*errors = append(*errors, &DataError{Type: "Validation error", Reason: "Type mismatch", Field: validator.Field, Value: "one of the indexes at least is not valid ObjectId: " + key})
					return false
				}
			}

			// or test the real value behind interface{}
			if vtype.String() != parts[1] {
				*errors = append(*errors, &DataError{Type: "Validation error", Reason: "Type mismatch", Field: validator.Field, Value: "one of the map values is of type: " + vtype.String()})
				return false
			}
		}
	default:
		if _type := reflect.TypeOf(valueToTest); _type != nil && _type.String() != validator.Type {
			// bson.ObjectId is match as a string, let's try to save them off the error pit
			if _type.String() == "string" && bson.IsObjectIdHex(valueToTest.(string)) {
				return true
			} else {
				// ok, let'em fall
				*errors = append(*errors, &DataError{Type: "Validation error", Reason: "Type mismatch", Field: validator.Field, Value: _type.String()})
				return false
			}
		}
	}
	return true
}
