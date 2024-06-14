#ifndef _HEADER_DFJOIN_H
#define _HEADER_DFJOIN_H

#include "zlib.h"
#include <stdlib.h>
#include <errno.h>
#include <string.h>

//see https://github.com/madler/zlib/blob/develop/examples/gzjoin.c

int initStream(z_stream *stream);
char *errMessage();

#endif /* _HEADER_DFJOIN_H */