export interface TransferDraft {
  direction: 'upload' | 'download'; paths: string[]; uploadDirectory: string;
  downloadPath: string; localDirectory: string; previousDirectory: string; previousWorkingPath: string;
}

export function transferDraftForContext(existing: TransferDraft | undefined, currentPath: string, workingPath: string, home: string): TransferDraft {
  const currentDirectory = workingPath || currentPath || home || '.'
  // Follow the terminal immediately, without waiting for the file listing.
  // Keep manual destinations until the terminal actually changes directory;
  // a refresh or delayed listing must not overwrite the user's selection.
  if (existing) {
    if (existing.previousWorkingPath !== currentDirectory) existing.uploadDirectory = currentDirectory
    existing.previousWorkingPath = currentDirectory
    return existing
  }
  return {
    direction: 'upload', paths: [], uploadDirectory: currentDirectory,
    downloadPath: currentDirectory, localDirectory: '', previousDirectory: currentDirectory,
    previousWorkingPath: currentDirectory,
  }
}

export function createUploadRequest(paths: readonly string[], destination: string): { paths: string[]; destination: string } {
  return { paths: [...paths], destination: destination.trim() }
}
