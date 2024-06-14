#include <string.h>
#include <errno.h>
#include "zlib.h"

int initStream(z_stream *stream) {
	stream->zalloc = Z_NULL;
	stream->zfree = Z_NULL;
	stream->opaque = Z_NULL;
	stream->avail_in = 0;
	stream->next_in = Z_NULL;
	return inflateInit2(stream, -15);
}

char *errMessage() {
	return strerror(errno);
}