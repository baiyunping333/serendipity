package serendipity

//	Initialize SQLite.  
//
//	This routine must be called to initialize the memory allocation, VFS, and mutex subsystems prior to doing any serious work with
//	SQLite.  But as long as you do not compile with SQLITE_OMIT_AUTOINIT this routine will be called automatically by key routines such as
//	Open().
//
//	This routine is a no-op except on its very first call for the process, or for the first call after a call to sqlite3_shutdown.
//
//	The first thread to call this routine runs the initialization to completion.  If subsequent threads call this routine before the first
//	thread has finished the initialization process, then the subsequent threads must block until the first thread finishes with the initialization.
//
//	The first thread might call this routine recursively.  Recursive calls to this routine should not block, of course.  Otherwise the
//	initialization process would never complete.
//
//	Let X be the first thread to enter this routine.  Let Y be some other thread.  Then while the initial invocation of this routine by X is
//	incomplete, it is required that:
//
//		*	Calls to this routine from Y must block until the outer-most call by X completes.
//
//		*	Recursive calls to this routine from thread X return immediately without blocking.

func sqlite3_initialize() (rc int) {
	//	If SQLite is already completely initialized, then this call to sqlite3_initialize() should be a no-op.  But the initialization
	//	must be complete.  So isInit must not be set until the very end of this routine.
	if sqlite3Config.isInit {
		return SQLITE_OK
	}

#ifdef SQLITE_ENABLE_SQLLOG
	extern void sqlite3_init_sqllog(void)
	sqlite3_init_sqllog()
#endif

	//	Make sure the mutex subsystem is initialized.  If unable to initialize the mutex subsystem, return early with the error.
	//	If the system is so sick that we are unable to allocate a mutex, there is not much SQLite is going to be able to do.
	//
	//	The mutex subsystem must take care of serializing its own initialization.
	if rc = sqlite3MutexInit(); rc != 0 {
		return rc
	}

	//	Initialize the malloc() system and the recursive pInitMutex mutex.
	//	This operation is protected by the STATIC_MASTER mutex.  Note that MutexAlloc() is called for a mutex prior to initializing the
	//	malloc subsystem - this implies that the allocation of a mutex must not require support from the malloc subsystem.
	MasterMutex := NewMutex(SQLITE_MUTEX_STATIC_MASTER)
	MasterMutex.CriticalSection(func() {
		sqlite3Config.isMutexInit = 1
		if !sqlite3Config.isMallocInit {
			rc = sqlite3MallocInit()
		}
		if rc == SQLITE_OK {
			sqlite3Config.isMallocInit = 1
			if !sqlite3Config.pInitMutex {
				sqlite3Config.pInitMutex = NewMutex(SQLITE_MUTEX_RECURSIVE)
				if sqlite3Config.bCoreMutex && !sqlite3Config.pInitMutex {
					rc = SQLITE_NOMEM
				}
			}
		}
		if rc == SQLITE_OK {
			sqlite3Config.nRefInitMutex++
		}
	})

	//	If rc is not SQLITE_OK at this point, then either the malloc subsystem could not be initialized or the system failed to allocate
	//	the pInitMutex mutex. Return an error in either case.
	if rc != SQLITE_OK {
		return rc
	}

	//	Do the rest of the initialization under the recursive mutex so that we will be able to handle recursive calls into
	//	sqlite3_initialize().  The recursive calls normally come through sqlite3_os_init() when it invokes sqlite3_vfs_register(), but other
	//	recursive calls might also be possible.
	//
	//	IMPLEMENTATION-OF: R-00140-37445 SQLite automatically serializes calls to the xInit method, so the xInit method need not be threadsafe.
	//
	//	The following mutex is what serializes access to the appdef pcache xInit methods.  The sqlite3_pcache_methods.xInit() all is embedded in the
	//	call to sqlite3PcacheInitialize().

	sqlite3Config.pInitMutex.CriticalSection(func() {
		if sqlite3Config.isInit == 0 && sqlite3Config.inProgress == 0 {
			pHash := sqlite3GlobalFunctions
			sqlite3Config.inProgress = 1
			memset(pHash, 0, sizeof(sqlite3GlobalFunctions))
			sqlite3RegisterGlobalFunctions();
			if sqlite3Config.isPCacheInit == 0 {
				rc = sqlite3PcacheInitialize()
			}
			if rc == SQLITE_OK {
				sqlite3Config.isPCacheInit = 1
				rc = sqlite3OsInit()
			}
			if rc == SQLITE_OK {
				sqlite3PCacheBufferSetup(sqlite3Config.pPage, sqlite3Config.szPage, sqlite3Config.nPage)
				sqlite3Config.isInit = 1
			}
			sqlite3Config.inProgress = 0
		}
	})

	//	Go back under the mutex and clean up the recursive mutex to prevent a resource leak.
	MasterMutex.CriticalSection(func() {
		sqlite3Config.nRefInitMutex--
		if sqlite3Config.nRefInitMutex <= 0 {
			assert( sqlite3Config.nRefInitMutex == 0 )
			sqlite3_mutex_free(sqlite3Config.pInitMutex)
			sqlite3Config.pInitMutex = 0
		}
	})

	//	The following is just a sanity check to make sure SQLite has been compiled correctly.  It is important to run this code, but
	//	we don't want to run it too often and soak up CPU cycles for no reason.  So we run it once during initialization.
#ifndef NDEBUG
	//	This section of code's only "output" is via assert() statements.
	if  rc == SQLITE_OK {
		u64 x = (((u64)(1)) << 63) - 1
		float64 y
		assert(sizeof(x) == 8)
		assert(sizeof(x) == sizeof(y))
		memcpy(&y, &x, 8)
		assert( math.IsNaN(y) )
	}
#endif

	//	Do extra initialization steps requested by the SQLITE_EXTRA_INIT compile-time option.
#ifdef SQLITE_EXTRA_INIT
	if rc == SQLITE_OK && sqlite3Config.isInit {
		int SQLITE_EXTRA_INIT(const char*)
		rc = SQLITE_EXTRA_INIT(0)
	}
#endif
	return
}


//	Undo the effects of sqlite3_initialize().  Must not be called while there are outstanding database connections or memory allocations or
//	while any part of SQLite is otherwise in use in any thread.  This routine is not threadsafe.  But it is safe to invoke this routine
//	on when SQLite is already shut down.  If SQLite is already shut down when this routine is invoked, then this routine is a harmless no-op.

func sqlite3_shutdown() int {
	if sqlite3Config.isInit {
#ifdef SQLITE_EXTRA_SHUTDOWN
		void SQLITE_EXTRA_SHUTDOWN(void)
		SQLITE_EXTRA_SHUTDOWN()
#endif
		sqlite3_os_end()
		sqlite3_reset_auto_extension()
		sqlite3Config.isInit = 0
	}
	if sqlite3Config.isPCacheInit {
		sqlite3PcacheShutdown()
		sqlite3Config.isPCacheInit = 0
	}
	if sqlite3Config.isMallocInit {
		sqlite3MallocEnd()
		sqlite3Config.isMallocInit = 0

#ifndef SQLITE_OMIT_SHUTDOWN_DIRECTORIES
		//	The heap subsystem has now been shutdown and these values are supposed to be NULL or point to memory that was obtained from sqlite3_malloc(),
		//	which would rely on that heap subsystem; therefore, make sure these values cannot refer to heap memory that was just invalidated when the
		//	heap subsystem was shutdown.  This is only done if the current call to this function resulted in the heap subsystem actually being shutdown.
		sqlite3_data_directory = 0
		sqlite3_temp_directory = 0
#endif
	}
	if sqlite3Config.isMutexInit {
		sqlite3MutexEnd()
		sqlite3Config.isMutexInit = 0
	}
	return SQLITE_OK
}


//	This API allows applications to modify the global configuration of the SQLite library at run-time.
//	This routine should only be called when there are no outstanding database connections or memory allocations.  This routine is not
//	threadsafe.  Failure to heed these warnings can lead to unpredictable behavior.

func sqlite3_config(op int, ap ...interface{}) (rc int) {
	rc = SQLITE_OK

	//	sqlite3_config() shall return SQLITE_MISUSE if it is invoked while the SQLite library is in use.
	if sqlite3Config.isInit {
		return SQLITE_MISUSE_BKPT
	}

	va_start(ap, op)
	switch op {
		//	Mutex configuration options are only available in a threadsafe compile. 
	case SQLITE_CONFIG_SINGLETHREAD:
		//	Disable all mutexing
		sqlite3Config.bCoreMutex = 0
		sqlite3Config.bFullMutex = 0
	case SQLITE_CONFIG_MULTITHREAD:
		//	Disable mutexing of database connections, enable mutexing of core data structures
		sqlite3Config.bCoreMutex = 1
		sqlite3Config.bFullMutex = 0
	case SQLITE_CONFIG_SERIALIZED:
		//	Enable all mutexing
		sqlite3Config.bCoreMutex = 1
		sqlite3Config.bFullMutex = 1
	case SQLITE_CONFIG_MUTEX:
		//	Specify an alternative mutex implementation
		sqlite3Config.mutex = *va_arg(ap, sqlite3_mutex_methods*)
	case SQLITE_CONFIG_GETMUTEX:
		//	Retrieve the current mutex implementation
		*va_arg(ap, sqlite3_mutex_methods*) = sqlite3Config.mutex
	case SQLITE_CONFIG_MALLOC:
		//	Specify an alternative malloc implementation
		sqlite3Config.m = *va_arg(ap, sqlite3_mem_methods*)
	case SQLITE_CONFIG_GETMALLOC:
		//	Retrieve the current malloc() implementation
		if sqlite3Config.m.xMalloc == 0 {
			sqlite3MemSetDefault()
		}
		*va_arg(ap, sqlite3_mem_methods*) = sqlite3Config.m
	case SQLITE_CONFIG_MEMSTATUS:
		//	Enable or disable the malloc status collection
		sqlite3Config.bMemstat = va_arg(ap, int)
	case SQLITE_CONFIG_SCRATCH:
		//	Designate a buffer for scratch memory space
		sqlite3Config.pScratch = va_arg(ap, void*)
		sqlite3Config.szScratch = va_arg(ap, int)
		sqlite3Config.nScratch = va_arg(ap, int)
	case SQLITE_CONFIG_PAGECACHE:
		//	Designate a buffer for page cache memory space
		sqlite3Config.pPage = va_arg(ap, void*)
		sqlite3Config.szPage = va_arg(ap, int)
		sqlite3Config.nPage = va_arg(ap, int)
	case SQLITE_CONFIG_PCACHE:
		//	no-op
	case SQLITE_CONFIG_GETPCACHE:
		//	now an error
		rc = SQLITE_ERROR
	case SQLITE_CONFIG_PCACHE2:
		//	Specify an alternative page cache implementation
		sqlite3Config.pcache2 = *va_arg(ap, sqlite3_pcache_methods2*)
	case SQLITE_CONFIG_GETPCACHE2:
		if sqlite3Config.pcache2.xInit == 0 {
			sqlite3PCacheSetDefault()
		}
		*va_arg(ap, sqlite3_pcache_methods2*) = sqlite3Config.pcache2
	//	Record a pointer to the logger funcction and its first argument. The default is NULL.  Logging is disabled if the function pointer is NULL.
	case SQLITE_CONFIG_LOG:
		//	MSVC is picky about pulling func ptrs from va lists.
		//	http://support.microsoft.com/kb/47961
		//	sqlite3Config.xLog = va_arg(ap, void(*)(void*,int,const char*))
		typedef void(*LOGFUNC_t)(void*,int,const char*)
		sqlite3Config.xLog = va_arg(ap, LOGFUNC_t)
		sqlite3Config.pLogArg = va_arg(ap, void*)
	case SQLITE_CONFIG_URI:
		sqlite3Config.bOpenUri = va_arg(ap, int)
	case SQLITE_CONFIG_COVERING_INDEX_SCAN:
		sqlite3Config.bUseCis = va_arg(ap, int)
#ifdef SQLITE_ENABLE_SQLLOG
	case SQLITE_CONFIG_SQLLOG:
		typedef void(*SQLLOGFUNC_t)(void*, sqlite3*, const char*, int)
		sqlite3Config.xSqllog = va_arg(ap, SQLLOGFUNC_t)
		sqlite3Config.pSqllogArg = va_arg(ap, void *)
#endif
	case SQLITE_CONFIG_MMAP_SIZE:
		sqlite3_int64 szMmap = va_arg(ap, sqlite3_int64)
		sqlite3_int64 mxMmap = va_arg(ap, sqlite3_int64)
		if mxMmap < 0 || mxMmap > SQLITE_MAX_MMAP_SIZE {
			mxMmap = SQLITE_MAX_MMAP_SIZE
		}
		sqlite3Config.mxMmap = mxMmap
		if szMmap < 0 {
			szMmap = SQLITE_DEFAULT_MMAP_SIZE
		}
		if szMmap > mxMmap {
			szMmap = mxMmap
		}
		sqlite3Config.szMmap = szMmap
    default:
		rc = SQLITE_ERROR
	}
	va_end(ap)
	return
}