package metrics

import "github.com/pkoukk/tiktoken-go"

type TokenCounter interface {
	Count(text string) int
}

type TiktokenCounter struct {
	tkm *tiktoken.Tiktoken
}

func NewTiktokenCounter(model string) (*TiktokenCounter, error) {
	tkm, err := tiktoken.EncodingForModel(model)
	if err != nil {
		tkm, err = tiktoken.GetEncoding("cl100k_base")
		if err != nil {
			return nil, err
		}
	}
	return &TiktokenCounter{tkm: tkm}, nil
}

func (c *TiktokenCounter) Count(text string) int {
	tokens := c.tkm.Encode(text, nil, nil)
	return len(tokens)
}

type LengthEstimator struct{}

func (c *LengthEstimator) Count(text string) int {
	return len(text) / 4
}
