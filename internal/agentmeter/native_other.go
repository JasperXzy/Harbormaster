//go:build !linux

package agentmeter

type Meter struct{}

func Open([]string) (*Meter, error)  { return nil, ErrUnsupported }
func (*Meter) Read() (Sample, error) { return Sample{}, ErrUnsupported }
func Remove([]string) error          { return ErrUnsupported }
