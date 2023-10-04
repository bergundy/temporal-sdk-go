package operation

type Error interface {
	error
	mustImplementErrorBase()
}

type UnauthorizedError struct {
	Message string
}

func (e UnauthorizedError) Error() string {
	return e.Message
}

func (UnauthorizedError) mustImplementErrorBase() {}

func NewUnauthorizedError(format string, args ...any) UnauthorizedError {
	return UnauthorizedError{}
}

type NotFoundError struct {
	Message string
}

func (e NotFoundError) Error() string {
	return e.Message
}

func (NotFoundError) mustImplementErrorBase() {}

func NewNotFoundError(format string, args ...any) NotFoundError {
	return NotFoundError{}
}

type BadRequestError struct {
	Message string
}

func (e BadRequestError) Error() string {
	return e.Message
}

func (BadRequestError) mustImplementErrorBase() {}

func NewBadRequestError(format string, args ...any) BadRequestError {
	return BadRequestError{}
}
