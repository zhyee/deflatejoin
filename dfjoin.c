#include "dfjoin.h"

int initStream(z_stream *stream) {
	stream->zalloc = Z_NULL;
	stream->zfree = Z_NULL;
	stream->opaque = Z_NULL;
	stream->avail_in = 0;
	stream->next_in = Z_NULL;
	return inflateInit2(stream, -15);
}

/* Raw inflate can satisfy a distance from bytes produced in the same call,
 * even when configured with a smaller history window. Restrict each call to
 * one output byte so every match is checked against the configured history.
 * Keep the loop in C and preserve the caller's block-boundary/output contract.
 * The common 32 KiB window continues to use the normal bulk inflate path. */
int inflateStream(z_stream *stream, int flush, int strict_window) {
	if (!strict_window || stream->avail_out == 0)
		return inflate(stream, flush);

	uInt remaining = stream->avail_out;
	for (;;) {
		uInt before = stream->avail_in;
		stream->avail_out = 1;
		int ret = inflate(stream, flush);
		uInt produced = 1 - stream->avail_out;
		remaining -= produced;
		stream->avail_out = remaining;
		if (ret != Z_OK || remaining == 0 ||
		    (flush == Z_BLOCK && (stream->data_type & 128)) ||
		    (produced == 0 && stream->avail_in == before))
			return ret;
	}
}
