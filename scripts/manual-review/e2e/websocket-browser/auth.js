export const authStore = { subscribe(fn) { fn({ token: '' }); return () => {}; } };
