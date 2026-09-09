#ifndef _HEADER_DFJOIN_H
#define _HEADER_DFJOIN_H

#include "zlib.h"
#include <stdlib.h>

//see https://github.com/madler/zlib/blob/develop/examples/gzjoin.c

int initStream(z_stream *stream);
int inflateStream(z_stream *stream, int flush, int strict_window);

#endif /* _HEADER_DFJOIN_H */
