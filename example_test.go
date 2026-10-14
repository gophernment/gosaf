package gosaf_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/gophernment/gosaf"
)

func Example() {
	cb, err := gosaf.New(gosaf.DefaultConfig())
	if err != nil {
		panic(err)
	}

	msg, err := gosaf.Do(context.Background(), cb, func(ctx context.Context) (string, error) {
		return "hello", nil // call your dependency here
	})
	if errors.Is(err, gosaf.ErrOpen) {
		fmt.Println("failing fast")
		return
	}
	fmt.Println(msg, err)
	// Output: hello <nil>
}
