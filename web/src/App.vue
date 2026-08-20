<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import AppSidebar from '@/components/AppSidebar.vue'
import AppTopBar from '@/components/AppTopBar.vue'
import CommandPalette from '@/components/CommandPalette.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import ToastHost from '@/components/ToastHost.vue'
import DrawerHost from '@/components/DrawerHost.vue'

const route = useRoute()
const bare = computed(() => route.meta.bare === true)
</script>

<template>
  <RouterView v-if="bare" />

  <div v-else class="shell">
    <AppSidebar />
    <div class="shell__main">
      <AppTopBar />
      <main class="shell__content">
        <RouterView v-slot="{ Component }">
          <Transition name="view-fade" mode="out-in">
            <component :is="Component" />
          </Transition>
        </RouterView>
      </main>
    </div>
    <DrawerHost />
    <ConfirmDialog />
    <CommandPalette />
    <ToastHost />
  </div>
</template>

<style scoped>
.shell { display: flex; height: 100%; overflow: hidden; }
.shell__main { flex: 1 1 auto; min-width: 0; display: flex; flex-direction: column; }
.shell__content { flex: 1 1 auto; overflow-y: auto; overflow-x: hidden; }
</style>
