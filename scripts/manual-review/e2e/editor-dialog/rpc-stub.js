const probe = { reads: [], writes: [] };

export const rpcClient = {
  file: {
    getFile(identity) {
      return new Promise((resolve, reject) => {
        probe.reads.push({
          ...identity,
          resolve: (text) => resolve({ content: new TextEncoder().encode(text) }),
          reject
        });
      });
    },
    updateFile(payload) {
      return new Promise((resolve, reject) => {
        probe.writes.push({
          serverId: payload.serverId, path: payload.path,
          content: new TextDecoder().decode(payload.content), resolve, reject
        });
      });
    }
  }
};
globalThis.__editorProbe = probe;
