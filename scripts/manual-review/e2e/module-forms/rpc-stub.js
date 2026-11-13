globalThis.__moduleCalls = [];
export const silentCallOptions = {};
export const rpcClient = {
  module: new Proxy(
    {},
    {
      get: (_target, method) => async (payload) => {
        if (method === "getNextAvailableModulePort") return { port: 8123 };
        if (method === "listModules") return { modules: [] };
        globalThis.__moduleCalls.push({
          method,
          payload: JSON.parse(JSON.stringify(payload)),
        });
        return {};
      },
    },
  ),
};
