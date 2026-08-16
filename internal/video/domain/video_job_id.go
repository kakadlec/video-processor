package domain

import "errors"

var ErrInvalidVideoJobID = errors.New("video: invalid video job id")

type VideoJobID struct {
	value string
}

func NewVideoJobID(value string) (VideoJobID, error) {
	if value == "" {
		return VideoJobID{}, ErrInvalidVideoJobID
	}
	return VideoJobID{value: value}, nil
}

func (id VideoJobID) String() string {
	return id.value
}

func (id VideoJobID) IsZero() bool {
	return id.value == ""
}

func (id VideoJobID) Equal(other VideoJobID) bool {
	return id.value == other.value
}

type VideoJobIDGenerator interface {
	NewVideoJobID() VideoJobID
}

type VideoJobIDParser interface {
	ParseVideoJobID(value string) (VideoJobID, error)
}
